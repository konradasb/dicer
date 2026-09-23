// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// InstanceStats is a point-in-time summary of the instances on this host,
// read when a scrape arrives.
type InstanceStats struct {
	// ByState counts instances per state, including zeros.
	ByState map[string]int

	// ByHealth counts the instances whose health is checked, by verdict
	// (starting, healthy, unhealthy), every verdict named as ByState's
	// states are.
	ByHealth map[string]int

	// VCPUs and MemoryBytes are the resources committed to instances:
	// those starting, running or paused.
	VCPUs       int
	MemoryBytes int64

	// AllocatableVCPUs and AllocatableMemoryBytes are what instances may be
	// committed in total, overcommit and reserve applied. A start that
	// would take the committed amount past them is refused.
	AllocatableVCPUs       int
	AllocatableMemoryBytes int64
}

// instanceMetrics measures the virtual machine lifecycle.
type instanceMetrics struct {
	operations *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	restarts   prometheus.Counter
}

func newInstanceMetrics() instanceMetrics {
	return instanceMetrics{
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "instance_operations_total",
			Help: "Instance lifecycle operations by operation " +
				"(start, stop, pause, resume, delete, create_snapshot, restore_snapshot, delete_snapshot) and outcome.",
		}, []string{"operation", "outcome"}),

		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "instance_operation_duration_seconds",
			Help:      "Time an instance lifecycle operation took.",
			// A start pulls an image and boots a guest; a stop waits on an
			// ACPI shutdown. Seconds, not milliseconds, is the right scale.
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 12),
		}, []string{"operation"}),

		restarts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "instance_restarts_total",
			Help:      "Instances started again by their restart policy after they ended.",
		}),
	}
}

func (im instanceMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{im.operations, im.duration, im.restarts}
}

// RecordInstanceOperation records a finished lifecycle operation and how long
// it took. The outcome is taken from err, so callers pass whatever they are
// about to return.
func (m *Metrics) RecordInstanceOperation(operation string, err error, d time.Duration) {
	m.instance.operations.WithLabelValues(operation, outcome(err)).Inc()
	m.instance.duration.WithLabelValues(operation).Observe(d.Seconds())
}

// RecordInstanceRestart records an instance being started again by its
// restart policy. Whether the restart succeeds is the start it makes.
func (m *Metrics) RecordInstanceRestart() {
	m.instance.restarts.Inc()
}

// instanceCollector exports the gauges describing the instances that exist
// right now. See collector.go for why these are read per scrape.
type instanceCollector struct {
	source func() InstanceStats

	count             *prometheus.Desc
	health            *prometheus.Desc
	vcpus             *prometheus.Desc
	memory            *prometheus.Desc
	allocatableVCPUs  *prometheus.Desc
	allocatableMemory *prometheus.Desc
}

func newInstanceCollector(source func() InstanceStats) *instanceCollector {
	return &instanceCollector{
		source: source,
		count: desc("instances",
			"Instances defined on this host, by lifecycle state.", "state"),
		health: desc("instances_health",
			"Running instances whose health is checked, by what the check has found.", "status"),
		vcpus: desc("instances_vcpus",
			"vCPUs committed to instances that are starting, running or paused."),
		memory: desc("instances_memory_bytes",
			"Guest memory committed to instances that are starting, running or paused."),
		allocatableVCPUs: desc("instances_vcpus_allocatable",
			"vCPUs instances may be committed in total; a start beyond it is refused."),
		allocatableMemory: desc("instances_memory_allocatable_bytes",
			"Guest memory instances may be committed in total; a start beyond it is refused."),
	}
}

func (c *instanceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.count
	ch <- c.health
	ch <- c.vcpus
	ch <- c.memory
	ch <- c.allocatableVCPUs
	ch <- c.allocatableMemory
}

func (c *instanceCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.source()

	for state, n := range stats.ByState {
		ch <- gauge(c.count, float64(n), state)
	}
	for status, n := range stats.ByHealth {
		ch <- gauge(c.health, float64(n), status)
	}
	ch <- gauge(c.vcpus, float64(stats.VCPUs))
	ch <- gauge(c.memory, float64(stats.MemoryBytes))
	ch <- gauge(c.allocatableVCPUs, float64(stats.AllocatableVCPUs))
	ch <- gauge(c.allocatableMemory, float64(stats.AllocatableMemoryBytes))
}
