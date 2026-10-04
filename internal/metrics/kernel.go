// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// KernelSummary is a point-in-time summary of the kernels defined on this
// host.
type KernelSummary struct {
	Count     int
	DiskBytes int64
}

// kernelMetrics measures fetching the kernels guests boot.
type kernelMetrics struct {
	fetches       *prometheus.CounterVec
	fetchDuration prometheus.Histogram
	fetchedBytes  prometheus.Counter
}

func (m *Metrics) newKernelMetrics() kernelMetrics {
	return kernelMetrics{
		fetches: m.counterVec(Description{
			Name:   "dicer_kernel_fetches_total",
			Labels: []string{"outcome"},
			Help:   "Kernel fetches, downloaded or copied from a local path, by outcome.",
			Doc:    "A kernel is fetched when an instance first starts with it. `outcome` is `success` or `error`.",
			Group:  GroupKernels,
		}),

		fetchDuration: m.histogram(Description{
			Name:  "dicer_kernel_fetch_duration_seconds",
			Help:  "Time a kernel fetch took, including verifying its checksum.",
			Group: GroupKernels,
		}, prometheus.ExponentialBuckets(0.25, 2, 12)),

		fetchedBytes: m.counter(Description{
			Name:  "dicer_kernel_fetched_bytes_total",
			Help:  "Bytes of kernels fetched, including fetches that failed.",
			Group: GroupKernels,
		}),
	}
}

// RecordKernelFetch records a kernel fetch: its outcome, duration and
// fetched bytes.
func (m *Metrics) RecordKernelFetch(err error, d time.Duration, fetchedBytes int64) {
	m.kernel.fetches.WithLabelValues(outcome(err)).Inc()
	m.kernel.fetchDuration.Observe(d.Seconds())
	m.kernel.fetchedBytes.Add(float64(fetchedBytes))
}

// kernelCollector exports the kernels defined and what they hold on disk.
// See collector.go for why this is read per scrape.
type kernelCollector struct {
	source func() KernelSummary

	count *prometheus.Desc
	bytes *prometheus.Desc
}

func (m *Metrics) newKernelCollector(source func() KernelSummary) *kernelCollector {
	return &kernelCollector{
		source: source,
		count: m.desc(Description{
			Name:  "dicer_kernels",
			Help:  "Kernels defined on this host.",
			Group: GroupKernels,
		}),
		bytes: m.desc(Description{
			Name:  "dicer_kernel_disk_bytes",
			Help:  "Total size of those kernels fetched to this host's disk.",
			Doc:   "A kernel not yet fetched counts as 0.",
			Group: GroupKernels,
		}),
	}
}

func (c *kernelCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.count
	ch <- c.bytes
}

func (c *kernelCollector) Collect(ch chan<- prometheus.Metric) {
	summary := c.source()

	ch <- gauge(c.count, float64(summary.Count))
	ch <- gauge(c.bytes, float64(summary.DiskBytes))
}
