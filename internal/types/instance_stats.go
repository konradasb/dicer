// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import "time"

// InstanceStats is what an instance's VMM process uses of the host, read
// from the host at one moment. Its vCPUs and the threads emulating its
// devices are counted together; totals are since the VMM started.
type InstanceStats struct {
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`

	// StartedAt is when the VMM was started. Totals read with another
	// StartedAt are of another VMM, and cannot be compared.
	StartedAt time.Time `json:"started_at"`

	// ReadAt is when the stats were read.
	ReadAt time.Time `json:"read_at"`

	// Committed is the vCPUs and guest memory committed to the instance.
	Committed Resources `json:"committed"`

	// CPUTime is the CPU time the VMM has used, user and system.
	CPUTime time.Duration `json:"cpu_time"`

	// ResidentMemoryBytes is the VMM's resident host memory: the guest
	// memory backed so far and its own. Memory a guest frees stays resident.
	ResidentMemoryBytes int64 `json:"resident_memory_bytes"`

	// DiskReadBytes and DiskWrittenBytes are what the VMM process read from
	// and wrote to storage: the guest's disks, and the VMM's own files, such
	// as the serial console log and a snapshot's memory. Reads served from
	// the page cache are not counted; writes are counted as the VMM makes
	// them, before they reach the disk.
	DiskReadBytes    int64 `json:"disk_read_bytes"`
	DiskWrittenBytes int64 `json:"disk_written_bytes"`

	// NetworkReceiveBytes and NetworkTransmitBytes are what the guest
	// received and transmitted on its network interface.
	NetworkReceiveBytes  int64 `json:"network_receive_bytes"`
	NetworkTransmitBytes int64 `json:"network_transmit_bytes"`

	// NetworkReceivePackets and NetworkTransmitPackets are the packets the
	// guest received and transmitted.
	NetworkReceivePackets  int64 `json:"network_receive_packets"`
	NetworkTransmitPackets int64 `json:"network_transmit_packets"`

	// NetworkReceiveDrops and NetworkTransmitDrops are the packets dropped on
	// their way to and from the guest. Receive drops mostly mean the guest
	// does not take packets as fast as they come.
	NetworkReceiveDrops  int64 `json:"network_receive_drops"`
	NetworkTransmitDrops int64 `json:"network_transmit_drops"`

	// NetworkReceiveErrors and NetworkTransmitErrors are the packets to and
	// from the guest that failed with an error.
	NetworkReceiveErrors  int64 `json:"network_receive_errors"`
	NetworkTransmitErrors int64 `json:"network_transmit_errors"`
}

// CPUPercent returns the CPU s used since prev, as a percentage of one host
// CPU: 200 is two CPUs kept busy. It is false if prev was read from another
// VMM, or not before s.
func (s InstanceStats) CPUPercent(prev InstanceStats) (float64, bool) {
	elapsed := s.ReadAt.Sub(prev.ReadAt)
	if !s.StartedAt.Equal(prev.StartedAt) || elapsed <= 0 {
		return 0, false
	}

	return float64(s.CPUTime-prev.CPUTime) / float64(elapsed) * 100, true
}
