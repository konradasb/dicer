// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import "github.com/prometheus/client_golang/prometheus"

// KernelSummary is a point-in-time summary of the kernels defined on this
// host.
type KernelSummary struct {
	Count     int
	DiskBytes int64
}

// kernelCollector exports the kernels defined and what they hold on disk.
// See collector.go for why this is read per scrape.
type kernelCollector struct {
	source func() KernelSummary

	count *prometheus.Desc
	disk  *prometheus.Desc
}

func (m *Metrics) newKernelCollector(source func() KernelSummary) *kernelCollector {
	return &kernelCollector{
		source: source,
		count: m.descriptor(Description{
			Name:  "dicer_kernels",
			Help:  "Kernels defined on this host.",
			Group: GroupKernels,
		}),
		disk: m.descriptor(Description{
			Name:  "dicer_kernel_disk_bytes",
			Help:  "Total size of those kernels on this host's disk.",
			Group: GroupKernels,
		}),
	}
}

func (c *kernelCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.count
	ch <- c.disk
}

func (c *kernelCollector) Collect(ch chan<- prometheus.Metric) {
	summary := c.source()

	ch <- gaugeReading(c.count, float64(summary.Count))
	ch <- gaugeReading(c.disk, float64(summary.DiskBytes))
}
