// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/konradasb/dicer/internal/types"
)

// instanceStatsLabels identify the instance a series is of. The ID stays the
// same across a rename; the name is what a person looks for.
var instanceStatsLabels = []string{"instance_id", "name"}

// instanceStatsCollector exports what each running or paused instance's VMM
// uses of the host. Its counters are the kernel's, read at scrape time, and
// begin again each time an instance starts, as Prometheus expects of a
// counter whose process restarts.
type instanceStatsCollector struct {
	source func() []types.InstanceStats

	cpu                    *prometheus.Desc
	vcpus                  *prometheus.Desc
	residentMemory         *prometheus.Desc
	memory                 *prometheus.Desc
	diskRead               *prometheus.Desc
	diskWritten            *prometheus.Desc
	networkReceive         *prometheus.Desc
	networkTransmit        *prometheus.Desc
	networkReceivePackets  *prometheus.Desc
	networkTransmitPackets *prometheus.Desc
	networkReceiveDrops    *prometheus.Desc
	networkTransmitDrops   *prometheus.Desc
	networkReceiveErrors   *prometheus.Desc
	networkTransmitErrors  *prometheus.Desc
}

func (m *Metrics) newInstanceStatsCollector(source func() []types.InstanceStats) *instanceStatsCollector {
	return &instanceStatsCollector{
		source: source,
		cpu: m.descriptor(Description{
			Name:   "dicer_instance_cpu_seconds_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "CPU time an instance's hypervisor process has used, user and system.",
			Doc:    "Its vCPUs and the threads emulating its devices together. `rate()` of it is the host CPUs it keeps busy.",
			Group:  GroupInstanceStats,
		}),
		vcpus: m.descriptor(Description{
			Name:   "dicer_instance_vcpus",
			Labels: instanceStatsLabels,
			Help:   "vCPUs committed to an instance.",
			Doc:    "`rate(dicer_instance_cpu_seconds_total[1m]) / dicer_instance_vcpus` is how busy it keeps them.",
			Group:  GroupInstanceStats,
		}),
		residentMemory: m.descriptor(Description{
			Name:   "dicer_instance_resident_memory_bytes",
			Labels: instanceStatsLabels,
			Help:   "Host memory resident for an instance's hypervisor process.",
			Doc:    "The guest memory backed so far, and the hypervisor's own. Memory a guest frees stays resident.",
			Group:  GroupInstanceStats,
		}),
		memory: m.descriptor(Description{
			Name:   "dicer_instance_memory_bytes",
			Labels: instanceStatsLabels,
			Help:   "Guest memory committed to an instance.",
			Doc:    "`dicer_instance_resident_memory_bytes / dicer_instance_memory_bytes` is how much of it is resident.",
			Group:  GroupInstanceStats,
		}),
		diskRead: m.descriptor(Description{
			Name:   "dicer_instance_disk_read_bytes_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Bytes an instance's hypervisor process read from storage.",
			Doc:    "The instance's disks, and the hypervisor's own files, such as the serial console log and a snapshot's memory. Reads served from the host's page cache are not counted.",
			Group:  GroupInstanceStats,
		}),
		diskWritten: m.descriptor(Description{
			Name:   "dicer_instance_disk_written_bytes_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Bytes an instance's hypervisor process wrote to storage.",
			Doc:    "The instance's disks, and the hypervisor's own files, such as the serial console log and a snapshot's memory. Counted as the process writes, before the data reaches the disk.",
			Group:  GroupInstanceStats,
		}),
		networkReceive: m.descriptor(Description{
			Name:   "dicer_instance_network_receive_bytes_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Bytes an instance's guest received on its network interface.",
			Group:  GroupInstanceStats,
		}),
		networkTransmit: m.descriptor(Description{
			Name:   "dicer_instance_network_transmit_bytes_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Bytes an instance's guest transmitted on its network interface.",
			Group:  GroupInstanceStats,
		}),
		networkReceivePackets: m.descriptor(Description{
			Name:   "dicer_instance_network_receive_packets_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Packets an instance's guest received on its network interface.",
			Group:  GroupInstanceStats,
		}),
		networkTransmitPackets: m.descriptor(Description{
			Name:   "dicer_instance_network_transmit_packets_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Packets an instance's guest transmitted on its network interface.",
			Group:  GroupInstanceStats,
		}),
		networkReceiveDrops: m.descriptor(Description{
			Name:   "dicer_instance_network_receive_drops_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Packets dropped on their way to an instance's guest.",
			Doc:    "Mostly the guest not taking packets as fast as they come.",
			Group:  GroupInstanceStats,
		}),
		networkTransmitDrops: m.descriptor(Description{
			Name:   "dicer_instance_network_transmit_drops_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Packets an instance's guest transmitted that the host dropped.",
			Group:  GroupInstanceStats,
		}),
		networkReceiveErrors: m.descriptor(Description{
			Name:   "dicer_instance_network_receive_errors_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Packets to an instance's guest that failed with an error.",
			Group:  GroupInstanceStats,
		}),
		networkTransmitErrors: m.descriptor(Description{
			Name:   "dicer_instance_network_transmit_errors_total",
			Type:   "counter",
			Labels: instanceStatsLabels,
			Help:   "Packets from an instance's guest that failed with an error.",
			Group:  GroupInstanceStats,
		}),
	}
}

func (c *instanceStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.cpu
	ch <- c.vcpus
	ch <- c.residentMemory
	ch <- c.memory
	ch <- c.diskRead
	ch <- c.diskWritten
	ch <- c.networkReceive
	ch <- c.networkTransmit
	ch <- c.networkReceivePackets
	ch <- c.networkTransmitPackets
	ch <- c.networkReceiveDrops
	ch <- c.networkTransmitDrops
	ch <- c.networkReceiveErrors
	ch <- c.networkTransmitErrors
}

func (c *instanceStatsCollector) Collect(ch chan<- prometheus.Metric) {
	for _, s := range c.source() {
		labels := []string{s.InstanceID, s.Name}

		ch <- counterReading(c.cpu, s.CPUTime.Seconds(), labels...)
		ch <- gaugeReading(c.vcpus, float64(s.Committed.VCPUs), labels...)
		ch <- gaugeReading(c.residentMemory, float64(s.ResidentMemoryBytes), labels...)
		ch <- gaugeReading(c.memory, float64(s.Committed.MemoryBytes), labels...)
		ch <- counterReading(c.diskRead, float64(s.DiskReadBytes), labels...)
		ch <- counterReading(c.diskWritten, float64(s.DiskWrittenBytes), labels...)
		ch <- counterReading(c.networkReceive, float64(s.NetworkReceiveBytes), labels...)
		ch <- counterReading(c.networkTransmit, float64(s.NetworkTransmitBytes), labels...)
		ch <- counterReading(c.networkReceivePackets, float64(s.NetworkReceivePackets), labels...)
		ch <- counterReading(c.networkTransmitPackets, float64(s.NetworkTransmitPackets), labels...)
		ch <- counterReading(c.networkReceiveDrops, float64(s.NetworkReceiveDrops), labels...)
		ch <- counterReading(c.networkTransmitDrops, float64(s.NetworkTransmitDrops), labels...)
		ch <- counterReading(c.networkReceiveErrors, float64(s.NetworkReceiveErrors), labels...)
		ch <- counterReading(c.networkTransmitErrors, float64(s.NetworkTransmitErrors), labels...)
	}
}
