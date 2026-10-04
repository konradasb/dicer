// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Package daemon assembles and runs dicerd: it loads the configuration,
// wires the services together and manages the process lifecycle.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/dns"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/hostinfo"
	"github.com/konradasb/dicer/internal/hostnet"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/initrd"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/metrics"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/registry"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/version"
	"github.com/konradasb/dicer/internal/vm"
	"github.com/konradasb/dicer/internal/volume"
)

// daemon is the Dicer host daemon. It manages one machine's virtual machines.
type daemon struct {
	cfg    *Config
	logger *slog.Logger

	definitions *filestore.Manager
	addresses   *network.Manager
	instances   *vm.Manager
	hostnet     *hostnet.Host
	// dnsServers serves each network's guests their nameserver. Nil if
	// the configuration turns it off.
	dnsServers *dns.Servers
	images     *image.Manager
	kernels    *kernel.Manager
	volumes    *volume.Manager
	initrds    *initrd.Manager

	// hypervisors are the starters for every VMM this daemon carries.
	hypervisors map[types.HypervisorType][]hypervisor.Starter

	metrics *metrics.Metrics
	events  *events.Log
}

func newDaemon(cfg *Config) (*daemon, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	level, err := cfg.logLevel()
	if err != nil {
		return nil, err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	d := &daemon{cfg: cfg, logger: logger}
	d.metrics = d.newMetrics()

	return d, nil
}

// Run starts the daemon and blocks until the context is cancelled.
func (d *daemon) Run(ctx context.Context) error {
	d.logger.Info("starting Dicer",
		"version", version.Version, "commit", version.Commit, "date", version.BuildDate)

	if err := d.openDefinitions(); err != nil {
		return err
	}

	if err := d.initServices(); err != nil {
		return err
	}

	// Deferred first so it runs last: the instance manager records events
	// until it is closed.
	defer func() {
		if err := d.events.Close(); err != nil {
			d.logger.Warn("not every event reached the events file", "error", err)
		}
	}()
	defer d.hostnet.Close()

	// Deferred before the instance manager's close, so it runs after: the
	// instance manager stops networks' DNS servers until it is closed.
	if d.dnsServers != nil {
		defer d.dnsServers.Close()
	}

	// Reconcile recorded state with what is running before serving.
	if err := d.instances.Recover(ctx); err != nil {
		return fmt.Errorf("recover instances: %w", err)
	}
	defer d.instances.Close()

	listeners, err := d.listenAPI(ctx)
	if err != nil {
		return err
	}

	serveErr := make(chan error, len(listeners))
	for _, l := range listeners {
		go func() {
			d.logger.Info("serving the API", "transport", l.transport, "address", l.address)
			if err := l.server.Serve(l.listener); err != nil {
				serveErr <- fmt.Errorf("serve the API on %s: %w", l.address, err)
			}
		}()
	}

	// Background work is waited for before the instance manager and events
	// log are closed.
	ctx, cancel := context.WithCancel(ctx)
	var background sync.WaitGroup
	defer background.Wait()
	defer cancel()

	metricsErr := make(chan error, 1)
	background.Go(func() {
		if err := d.serveMetrics(ctx); err != nil {
			metricsErr <- err
		}
	})

	background.Go(func() { d.hostnet.WatchFirewalld(ctx) })

	// Started after the API is up so slow boots do not delay it.
	background.Go(func() { d.instances.StartOnBoot(ctx) })

	// Garbage collection needs recovery to know which images are in use.
	if policy := d.cfg.Images.gcPolicy(); policy.Enabled() {
		background.Go(func() {
			d.images.RunGC(ctx, policy, d.cfg.Images.GCInterval, d.instances.ImagesInUse)
		})
	}

	select {
	case err := <-serveErr:
		return err
	case err := <-metricsErr:
		return err
	case <-ctx.Done():
	}

	d.logger.Info("shutting down")
	stopServers(listeners, apiDrainTimeout)

	// Running VMs outlive the daemon; the next start re-adopts them.
	d.logger.Info("stopped, leaving running instances alone")
	return nil
}

// apiDrainTimeout bounds how long a shutdown waits for calls in flight, such
// as followed logs or exec sessions.
const apiDrainTimeout = 10 * time.Second

// stopServers stops every server concurrently with stopServer.
func stopServers(listeners []apiListener, timeout time.Duration) {
	var wg sync.WaitGroup
	for _, l := range listeners {
		wg.Go(func() { stopServer(l.server, timeout) })
	}
	wg.Wait()
}

// stopServer stops srv gracefully, forcing it after timeout.
func stopServer(srv *grpc.Server, timeout time.Duration) {
	stopped := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(timeout):
		srv.Stop()
		<-stopped
	}
}

func (d *daemon) openDefinitions() error {
	s, err := filestore.NewManager(filestore.Config{
		DataDir: d.cfg.DataDir,
		Logger:  d.logger,
	})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	d.definitions = s

	a, err := network.NewManager(network.Config{
		Dir:    filepath.Join(d.cfg.DataDir, "allocations"),
		Logger: d.logger,
	})
	if err != nil {
		return fmt.Errorf("open address manager: %w", err)
	}
	d.addresses = a

	return nil
}

// eventsFile is the events log, in the data directory.
const eventsFile = "events.jsonl"

func (d *daemon) initServices() error {
	var err error
	d.events, err = events.Open(events.Config{
		File:     filepath.Join(d.cfg.DataDir, eventsFile),
		MaxCount: d.cfg.Events.MaxCount,
		MaxAge:   d.cfg.Events.MaxAge,
		Logger:   d.logger,
	})
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}

	auths := make(map[string]registry.Auth, len(d.cfg.Registries))
	for host, r := range d.cfg.Registries {
		auths[host] = r.auth()
	}
	registryClient, err := registry.NewClient(d.cfg.DataDir,
		registry.WithLogger(d.logger), registry.WithKeychain(registry.NewKeychain(auths)))
	if err != nil {
		return fmt.Errorf("create registry client: %w", err)
	}

	d.images, err = image.NewManager(image.Config{
		DataDir:            d.cfg.DataDir,
		MaxConcurrentPulls: 1,
		Registry:           registryClient,
		Events:             d.events,
		Metrics:            d.metrics,
		Logger:             d.logger,
	})
	if err != nil {
		return fmt.Errorf("create image store: %w", err)
	}

	d.hostnet = hostnet.NewHost(hostnet.Config{
		UplinkInterface:         d.cfg.Network.UplinkInterface,
		UplinkCapacityBps:       d.cfg.Network.UplinkCapacityBps,
		UploadBurstMultiplier:   d.cfg.Network.UploadBurstMultiplier,
		DownloadBurstMultiplier: d.cfg.Network.DownloadBurstMultiplier,
		Logger:                  d.logger,
	})

	d.kernels, err = kernel.NewManager(kernel.Config{
		DataDir: d.cfg.DataDir,
		Metrics: d.metrics,
		Logger:  d.logger,
	})
	if err != nil {
		return fmt.Errorf("create kernel store: %w", err)
	}

	d.initrds, err = initrd.NewManager(initrd.Config{
		Puller:  registryClient,
		DataDir: d.cfg.DataDir,
		Logger:  d.logger,
	})
	if err != nil {
		return fmt.Errorf("create initrd manager: %w", err)
	}

	d.volumes = volume.NewManager(volume.Config{
		DataDir: d.cfg.DataDir,
		Logger:  d.logger,
	})

	d.hypervisors, err = buildStarters(d.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("build hypervisor starters: %w", err)
	}

	capacity, err := d.hostCapacity()
	if err != nil {
		return err
	}

	vmCfg := vm.Config{
		Definitions: d.definitions,
		Addresses:   d.addresses,
		RunDir:      d.cfg.RunDir,
		Images:      d.images,
		Kernels:     d.kernels,
		Volumes:     d.volumes,
		Initrds:     d.initrds,
		HostNetwork: d.hostnet,
		Starters:    d.hypervisors,
		Capacity:    capacity,
		Metrics:     d.metrics,
		Events:      d.events,
		Logger:      d.logger,
	}
	if d.cfg.Network.DNS {
		// The servers ask the instance manager about the networks'
		// instances, and it starts and stops the servers.
		d.dnsServers = dns.NewServers(dns.Config{
			Resolver:           instanceNames{d},
			Metrics:            d.metrics,
			DefaultNameservers: []string{network.DefaultNameserver},
			Logger:             d.logger,
		})
		vmCfg.DNSServers = d.dnsServers
	}
	d.instances = vm.NewManager(vmCfg)

	return nil
}

// instanceNames answers the DNS servers' questions about the networks'
// instances, from the instance manager, which is made after them.
type instanceNames struct{ d *daemon }

// LookupHost asks the instance manager.
func (n instanceNames) LookupHost(network, name string) []netip.Addr {
	return n.d.instances.LookupHost(network, name)
}

// LookupAddr asks the instance manager.
func (n instanceNames) LookupAddr(network string, addr netip.Addr) []string {
	return n.d.instances.LookupAddr(network, addr)
}

// hostCapacity reads the host's CPUs and memory once and works out what
// instances may be given.
func (d *daemon) hostCapacity() (types.Capacity, error) {
	cpus, err := hostinfo.CPUCount()
	if err != nil {
		return types.Capacity{}, fmt.Errorf("read the host's CPUs: %w", err)
	}
	mem, err := hostinfo.MemoryTotal()
	if err != nil {
		return types.Capacity{}, fmt.Errorf("read the host's memory: %w", err)
	}

	capacity, err := d.cfg.Resources.capacity(cpus, mem)
	if err != nil {
		return types.Capacity{}, err
	}

	allocatable := capacity.Allocatable()
	d.logger.Info("admission capacity",
		"cpus", cpus, "memory_bytes", mem,
		"cpu_overcommit", capacity.CPUOvercommit, "memory_overcommit", capacity.MemoryOvercommit,
		"reserved_memory_bytes", capacity.ReservedMemoryBytes,
		"allocatable_vcpus", allocatable.VCPUs, "allocatable_memory_bytes", allocatable.MemoryBytes)

	return capacity, nil
}
