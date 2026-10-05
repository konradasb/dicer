// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// InstanceSummary is a point-in-time summary of the instances on this host,
// read when a scrape arrives.
type InstanceSummary struct {
	// ByState counts instances per state, including zeros, each state named
	// in lower case: running, stopped.
	ByState map[string]int

	// ByHealth counts the instances whose health is checked, by verdict
	// (starting, healthy, unhealthy), named in lower case as ByState's
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

func (m *Metrics) newInstanceMetrics() instanceMetrics {
	return instanceMetrics{
		operations: m.counterVec(Description{
			Name:   "dicer_instance_operations_total",
			Labels: []string{"operation", "outcome"},
			Help: "Instance lifecycle operations by operation " +
				"(start, stop, pause, resume, standby, resize, delete, create_snapshot, restore_snapshot, delete_snapshot, fork_snapshot) and outcome.",
			Doc:   "`outcome` is `success` or `error`.",
			Group: GroupInstances,
		}),

		// A start pulls an image and boots a guest; a stop waits on an ACPI
		// shutdown. Seconds, not milliseconds, is the right scale.
		duration: m.histogramVec(Description{
			Name:   "dicer_instance_operation_duration_seconds",
			Labels: []string{"operation"},
			Help:   "Time an instance lifecycle operation took.",
			Group:  GroupInstances,
		}, prometheus.ExponentialBuckets(0.05, 2, 12)),

		restarts: m.counter(Description{
			Name:  "dicer_instance_restarts_total",
			Help:  "Instances started again by their restart policy after they ended.",
			Group: GroupInstances,
		}),
	}
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
	source func() InstanceSummary

	count             *prometheus.Desc
	health            *prometheus.Desc
	vcpus             *prometheus.Desc
	memory            *prometheus.Desc
	allocatableVCPUs  *prometheus.Desc
	allocatableMemory *prometheus.Desc
}

func (m *Metrics) newInstanceCollector(source func() InstanceSummary) *instanceCollector {
	return &instanceCollector{
		source: source,
		count: m.descriptor(Description{
			Name:   "dicer_instances",
			Labels: []string{"state"},
			Help:   "Instances defined on this host, by lifecycle state.",
			Doc:    "Every state is present, at 0 if none.",
			Group:  GroupInstances,
		}),
		health: m.descriptor(Description{
			Name:   "dicer_instances_health",
			Labels: []string{"status"},
			Help:   "Running instances whose health is checked, by what the check has found.",
			Doc:    "`status` is `starting`, `healthy` or `unhealthy`.",
			Group:  GroupInstances,
		}),
		vcpus: m.descriptor(Description{
			Name:  "dicer_instances_vcpus",
			Help:  "vCPUs committed to instances that are starting, running or paused.",
			Group: GroupInstances,
		}),
		memory: m.descriptor(Description{
			Name:  "dicer_instances_memory_bytes",
			Help:  "Guest memory committed to instances that are starting, running or paused.",
			Group: GroupInstances,
		}),
		allocatableVCPUs: m.descriptor(Description{
			Name:  "dicer_instances_vcpus_allocatable",
			Help:  "vCPUs instances may be committed in total; a start beyond it is refused.",
			Group: GroupInstances,
		}),
		allocatableMemory: m.descriptor(Description{
			Name:  "dicer_instances_memory_allocatable_bytes",
			Help:  "Guest memory instances may be committed in total; a start beyond it is refused.",
			Group: GroupInstances,
		}),
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
	summary := c.source()

	for state, n := range summary.ByState {
		ch <- gaugeReading(c.count, float64(n), state)
	}
	for status, n := range summary.ByHealth {
		ch <- gaugeReading(c.health, float64(n), status)
	}
	ch <- gaugeReading(c.vcpus, float64(summary.VCPUs))
	ch <- gaugeReading(c.memory, float64(summary.MemoryBytes))
	ch <- gaugeReading(c.allocatableVCPUs, float64(summary.AllocatableVCPUs))
	ch <- gaugeReading(c.allocatableMemory, float64(summary.AllocatableMemoryBytes))
}
