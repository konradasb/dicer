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

	"github.com/konradasb/dicer/internal/metrics"
	"github.com/konradasb/dicer/internal/version"
)

const (
	// metricsPath is where the Prometheus endpoint is served.
	metricsPath = "/metrics"

	// metricsShutdownTimeout bounds how long a shutdown waits for an
	// in-flight scrape.
	metricsShutdownTimeout = 2 * time.Second

	// metricsReadHeaderTimeout bounds how long a scraper may take to send
	// its request headers.
	metricsReadHeaderTimeout = 5 * time.Second
)

// newMetrics builds the metrics this daemon records into, whether or not the
// endpoint is served. The scrape-time sources read managers that
// initServices creates later.
func (d *daemon) newMetrics() *metrics.Metrics {
	return metrics.New(metrics.Options{
		Version: version.Version,
		Commit:  version.Commit,
		Logger:  d.logger.With("component", "metrics"),
		Sources: metrics.Sources{
			Instances: d.instanceStats,
			Networks:  d.networkStats,
			Images:    d.imageStats,
		},
	})
}

// instanceStats reads the current instance counts for a scrape. It reports
// nothing before the instance manager exists.
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

// networkStats reads each network's address pool usage for a scrape. A
// network whose allocations cannot be read is skipped.
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

// imageStats sums what the image store holds for a scrape. It reports
// nothing before the store exists.
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

// serveMetrics serves the metrics endpoint until ctx is cancelled. It returns
// at once if the endpoint is disabled.
func (d *daemon) serveMetrics(ctx context.Context) error {
	cfg := d.cfg.Metrics
	if !cfg.Enable {
		return nil
	}

	logger := d.logger.With("component", "metrics")

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	mux := http.NewServeMux()
	mux.Handle(metricsPath, d.metrics.Handler())

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: metricsReadHeaderTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("serving metrics", "listen", cfg.Listen, "path", metricsPath)

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

	// ctx is already done, so the drain needs its own deadline.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("metrics server did not shut down cleanly", "error", err)
	}

	return <-serveErr
}
