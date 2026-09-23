// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Package daemon assembles and runs dicerd.
//
// It owns configuration, wires the services together, and manages the process
// lifecycle. It is the only package that knows how the pieces fit: everything
// below it is written against interfaces or concrete types it is handed.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/access"
	"github.com/dicer-sh/dicer/internal/events"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/hostinfo"
	"github.com/dicer-sh/dicer/internal/hostnet"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	"github.com/dicer-sh/dicer/internal/image"
	"github.com/dicer-sh/dicer/internal/initrd"
	"github.com/dicer-sh/dicer/internal/kernel"
	"github.com/dicer-sh/dicer/internal/metrics"
	"github.com/dicer-sh/dicer/internal/network"
	"github.com/dicer-sh/dicer/internal/registry"
	"github.com/dicer-sh/dicer/internal/vm"
	"github.com/dicer-sh/dicer/internal/volume"
)

// daemon is the Dicer host daemon. It owns one machine's virtual machines:
// no peers, no leader election, no membership. To manage several hosts, run
// one of these on each.
type daemon struct {
	cfg    *Config
	logger *slog.Logger

	definitions *filestore.Manager
	addresses   *network.Manager
	instances   *vm.Manager
	hostnet     *hostnet.Host
	images      *image.Manager
	kernels     *kernel.Manager
	volumes     *volume.Manager
	initrds     *initrd.Manager

	// hypervisors are the starters for every VMM this daemon carries.
	hypervisors map[dicer.HypervisorType][]hypervisor.Starter

	// access decides which clients may use the API over the network.
	access *access.Manager

	// metrics is what every measured operation records into. The
	// configuration decides whether it is also served over HTTP.
	metrics *metrics.Metrics

	// events is where what happens to instances and images is recorded.
	events *events.Log
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
		"version", dicer.Version, "commit", dicer.Commit, "date", dicer.BuildDate)

	if err := d.openDefinitions(); err != nil {
		return err
	}

	if err := d.initServices(); err != nil {
		return err
	}

	// Reconcile recorded state against what is actually running before
	// serving, so the first client to connect sees the truth. From here on
	// the manager watches every running VMM, and Close stops it watching.
	// Deferred before anything else that is deferred, it runs last, after
	// the API has stopped taking requests.
	defer func() { _ = d.events.Close() }() // last: the lifecycle records until it stops
	if err := d.instances.Recover(ctx); err != nil {
		return fmt.Errorf("recover instances: %w", err)
	}
	defer d.instances.Close()

	servers, err := d.listenAPI(ctx)
	if err != nil {
		return err
	}

	serveErr := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			d.logger.Info("serving the API", "transport", srv.transport, "address", srv.address)
			if err := srv.server.Serve(srv.listener); err != nil {
				serveErr <- fmt.Errorf("serve the API on %s: %w", srv.address, err)
			}
		}()
	}

	// The work started below ends when ctx does, and is waited for before
	// the instance manager and the events log it uses are closed.
	ctx, cancel := context.WithCancel(ctx)
	var background sync.WaitGroup
	defer background.Wait()
	defer cancel()

	// Only a failure arrives here; a disabled endpoint serves nothing and
	// reports nothing.
	metricsErr := make(chan error, 1)
	background.Go(func() {
		if err := d.serveMetrics(ctx); err != nil {
			metricsErr <- err
		}
	})

	// StartOnBoot runs after the API is up, so a slow-booting instance does not
	// delay the daemon becoming usable.
	background.Go(func() { d.instances.StartOnBoot(ctx) })

	// Garbage collection runs after recovery, when what is in use is
	// known.
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
	for _, srv := range servers {
		stopServer(srv.server, apiDrainTimeout)
	}

	// Running VMs are deliberately left running. They are separate processes
	// and survive the daemon; recovery re-adopts them on the next start.
	d.logger.Info("stopped, leaving running instances alone")
	return nil
}

// apiDrainTimeout bounds how long a shutdown waits for calls in flight. A
// stream such as a followed log or an exec session lasts as long as its
// client keeps it open, and must not hold the daemon up past this.
const apiDrainTimeout = 10 * time.Second

// stopServer stops srv gracefully, letting calls in flight finish, and after
// timeout stops it outright, ending those that have not.
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

	// Addresses are kept in their own table, separate from the definition
	// store: address policy is not storage.
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
		Path:     filepath.Join(d.cfg.DataDir, eventsFile),
		MaxCount: d.cfg.Events.MaxCount,
		MaxAge:   d.cfg.Events.MaxAge,
		Logger:   d.logger,
	})
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}

	registryClient, err := registry.NewClient(d.cfg.DataDir, registry.WithLogger(d.logger))
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

	d.instances = vm.NewManager(vm.Config{
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
	})

	return nil
}

// hostCapacity reads the host's CPUs and memory, once, and works out what
// instances may be given of them.
//
// Read once rather than on every start: neither changes under a running
// daemon short of hotplug, and a start is not the moment to discover that
// /proc cannot be read.
func (d *daemon) hostCapacity() (dicer.Capacity, error) {
	cpus, err := hostinfo.CPUCount()
	if err != nil {
		return dicer.Capacity{}, fmt.Errorf("read the host's CPUs: %w", err)
	}
	mem, err := hostinfo.MemoryTotal()
	if err != nil {
		return dicer.Capacity{}, fmt.Errorf("read the host's memory: %w", err)
	}

	capacity, err := d.cfg.Resources.capacity(cpus, mem)
	if err != nil {
		return dicer.Capacity{}, err
	}

	allocatable := capacity.Allocatable()
	d.logger.Info("admission capacity",
		"cpus", cpus, "memory_bytes", mem,
		"cpu_overcommit", capacity.CPUOvercommit, "memory_overcommit", capacity.MemoryOvercommit,
		"reserved_memory_bytes", capacity.ReservedMemoryBytes,
		"allocatable_vcpus", allocatable.VCPUs, "allocatable_memory_bytes", allocatable.MemoryBytes)

	return capacity, nil
}
