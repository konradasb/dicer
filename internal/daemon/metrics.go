// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/metrics"
)

const (
	// metricsShutdownTimeout bounds how long a shutdown waits for an
	// in-flight scrape. A scrape that has not finished by then is not worth
	// delaying the daemon's exit for.
	metricsShutdownTimeout = 2 * time.Second

	// metricsReadHeaderTimeout bounds how long a scraper may take to send
	// its request headers. A scrape comes from a monitoring system on this
	// host rather than from the internet, but an unbounded read is still a
	// way to pin a connection open indefinitely.
	metricsReadHeaderTimeout = 5 * time.Second
)

// newMetrics builds the metrics this daemon records into.
//
// They are built whether or not the endpoint is served: recording costs a few
// atomic adds on operations that boot virtual machines, and a counter that is
// only correct while someone is scraping is a counter nobody can trust.
//
// The scrape-time sources are closures over this daemon rather than the
// managers themselves, because internal/vm and internal/image do not know
// this package's types and should not have to. They are called long after
// this returns, by which time initServices has filled the fields in.
func (d *daemon) newMetrics() *metrics.Metrics {
	return metrics.New(metrics.Options{
		Version: dicer.Version,
		Commit:  dicer.Commit,
		Logger:  d.logger.With("component", "metrics"),
		Sources: metrics.Sources{
			Instances: d.instanceStats,
			Networks:  d.networkStats,
			Images:    d.imageStats,
		},
	})
}

// instanceStats reads the current instance counts for a scrape.
//
// The managers are built after this closure is registered, and the endpoint
// is only served once they exist -- but a source that depends on that
// ordering to avoid a nil dereference is a source that breaks the first time
// startup is reordered, so it reports nothing instead.
func (d *daemon) instanceStats() metrics.InstanceStats {
	if d.instances == nil {
		return metrics.InstanceStats{}
	}

	usage := d.instances.Usage()

	byState := make(map[string]int, len(usage.ByState))
	for state, n := range usage.ByState {
		byState[state.String()] = n
	}
	byHealth := make(map[string]int, len(usage.ByHealth))
	for status, n := range usage.ByHealth {
		byHealth[string(status)] = n
	}

	allocatable := usage.Capacity.Allocatable()

	return metrics.InstanceStats{
		ByState:                byState,
		ByHealth:               byHealth,
		VCPUs:                  usage.Allocated.VCPUs,
		MemoryBytes:            usage.Allocated.MemoryBytes,
		AllocatableVCPUs:       allocatable.VCPUs,
		AllocatableMemoryBytes: allocatable.MemoryBytes,
	}
}

// networkStats reads each network's address pool usage for a scrape.
//
// It lives here rather than in internal/network because it is a join: the
// defined networks come from the store and their allocations from the
// address manager, which deliberately knows nothing about either. A network whose
// allocations cannot be read is skipped rather than reported as empty, since
// a pool that silently reads as free is worse than one that reads as absent.
func (d *daemon) networkStats() []metrics.NetworkStats {
	if d.definitions == nil || d.addresses == nil {
		return nil
	}

	networks, err := d.definitions.ListNetworks()
	if err != nil {
		d.logger.Warn("cannot list networks for metrics", "error", err)
		return nil
	}

	stats := make([]metrics.NetworkStats, 0, len(networks))
	for _, nw := range networks {
		allocations, err := d.addresses.List(nw.Name)
		if err != nil {
			d.logger.Warn("cannot read allocations for metrics",
				"network", nw.Name, "error", err)
			continue
		}

		_, available := nw.Usage(len(allocations))
		stats = append(stats, metrics.NetworkStats{
			Name:      nw.Name,
			Allocated: len(allocations),
			Available: available,
		})
	}

	return stats
}

// imageStats sums what the image store holds for a scrape. Like
// instanceStats, it reports nothing before the store exists.
func (d *daemon) imageStats() metrics.ImageStats {
	if d.images == nil {
		return metrics.ImageStats{}
	}

	images := d.images.List()

	stats := metrics.ImageStats{Count: len(images)}
	for _, img := range images {
		stats.DiskBytes += img.SizeBytes
	}

	return stats
}

// serveMetrics serves the metrics endpoint until ctx is cancelled, returning
// once the server has stopped. With the endpoint disabled it returns
// immediately, having served nothing.
//
// Unlike the API socket this is a TCP listener, so a failure to bind is
// returned rather than logged: a scrape target that is silently absent is how
// a monitoring gap starts.
func (d *daemon) serveMetrics(ctx context.Context) error {
	cfg := d.cfg.Metrics
	if !cfg.Enabled {
		return nil
	}

	logger := d.logger.With("component", "metrics")

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	mux := http.NewServeMux()
	mux.Handle(cfg.Path, d.metrics.Handler())

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: metricsReadHeaderTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("serving metrics", "listen", cfg.Listen, "path", cfg.Path)

		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serve metrics: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	// The context that stopped us cannot also bound the drain, or the drain
	// would be over before it began.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("metrics server did not shut down cleanly", "error", err)
	}

	return <-serveErr
}
