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
	"github.com/konradasb/dicer/internal/types"
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
// openDefinitionsAndAllocations and initServices create later.
func (d *daemon) newMetrics() *metrics.Metrics {
	return metrics.New(metrics.Options{
		Version: version.Version,
		Commit:  version.Commit,
		Logger:  d.logger.With("component", "metrics"),
		Sources: metrics.Sources{
			Instances:     d.instanceSummary,
			InstanceStats: d.instanceStats,
			Networks:      d.networkSummaries,
			Images:        d.imageSummary,
			Kernels:       d.kernelSummary,
			Volumes:       d.volumeSummary,
		},
	})
}

// instanceSummary reads the current instance counts for a scrape. It reports
// nothing before the instance manager exists.
func (d *daemon) instanceSummary() metrics.InstanceSummary {
	if d.instances == nil {
		return metrics.InstanceSummary{}
	}

	usage := d.instances.Usage()

	byState := make(map[string]int, len(usage.ByState))
	for state, n := range usage.ByState {
		byState[state.Lowercase()] = n
	}
	byHealth := make(map[string]int, len(usage.ByHealth))
	for status, n := range usage.ByHealth {
		byHealth[string(status)] = n
	}

	allocatable := usage.Capacity.Allocatable()

	return metrics.InstanceSummary{
		ByState:                byState,
		ByHealth:               byHealth,
		VCPUs:                  usage.Allocated.VCPUs,
		MemoryBytes:            usage.Allocated.MemoryBytes,
		AllocatableVCPUs:       allocatable.VCPUs,
		AllocatableMemoryBytes: allocatable.MemoryBytes,
	}
}

// instanceStats reads what each instance uses of the host for a scrape. It
// reports nothing before the instance manager exists.
func (d *daemon) instanceStats() []types.InstanceStats {
	if d.instances == nil {
		return nil
	}

	return d.instances.Stats()
}

// networkSummaries reads each network's address pool usage for a scrape. A
// network whose allocations cannot be read is skipped.
func (d *daemon) networkSummaries() []metrics.NetworkSummary {
	if d.definitions == nil || d.networks == nil {
		return nil
	}

	networks := d.definitions.Networks()

	summaries := make([]metrics.NetworkSummary, 0, len(networks))
	for _, network := range networks {
		allocations, err := d.networks.List(network.Name)
		if err != nil {
			d.logger.Warn("cannot read allocations for metrics",
				"network", network.Name, "error", err)
			continue
		}

		_, available := network.IPCounts(len(allocations))
		summaries = append(summaries, metrics.NetworkSummary{
			Name:      network.Name,
			Allocated: len(allocations),
			Available: available,
		})
	}

	return summaries
}

// imageSummary counts the images pulled, and sums their sizes, for a scrape.
// It reports nothing before the image manager exists.
func (d *daemon) imageSummary() metrics.ImageSummary {
	if d.images == nil {
		return metrics.ImageSummary{}
	}

	images := d.images.List()

	summary := metrics.ImageSummary{Count: len(images)}
	for _, image := range images {
		summary.DiskBytes += image.SizeBytes
	}

	return summary
}

// kernelSummary counts the kernels defined, and sums what they hold on disk,
// for a scrape. It reports nothing before the kernel manager exists.
func (d *daemon) kernelSummary() metrics.KernelSummary {
	if d.definitions == nil || d.kernels == nil {
		return metrics.KernelSummary{}
	}

	kernels := d.definitions.Kernels()

	summary := metrics.KernelSummary{Count: len(kernels)}
	for _, k := range kernels {
		summary.DiskBytes += d.kernels.DiskBytes(k.ID)
	}

	return summary
}

// volumeSummary counts the volumes defined, and sums their sizes and what
// they take up on disk, for a scrape. It reports nothing before the volume
// manager exists.
func (d *daemon) volumeSummary() metrics.VolumeSummary {
	if d.definitions == nil || d.volumes == nil {
		return metrics.VolumeSummary{}
	}

	volumes := d.definitions.Volumes()

	summary := metrics.VolumeSummary{Count: len(volumes)}
	for _, v := range volumes {
		summary.SizeBytes += v.SizeBytes
		summary.DiskBytes += d.volumes.DiskBytes(v.ID)
	}

	return summary
}

// serveMetrics serves the metrics endpoint until ctx is cancelled. It returns
// at once if the endpoint is disabled.
func (d *daemon) serveMetrics(ctx context.Context) error {
	cfg := d.cfg.Metrics
	if !cfg.Enable {
		return nil
	}

	logger := d.logger.With("component", "metrics")

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
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
