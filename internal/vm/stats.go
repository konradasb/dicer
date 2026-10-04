// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"fmt"
	"time"

	"github.com/prometheus/procfs"

	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
)

// Stats are read from the host alone, so they need nothing of the guest and
// are the same for every hypervisor: CPU, memory and disk from the VMM's
// entries in /proc, network from its TAP device's counters.

// Stats returns what the VMM of each running or paused instance uses of the
// host now, in name order. An instance whose VMM cannot be read is left out.
func (m *Manager) Stats() []types.InstanceStats {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		m.logger.Warn("cannot list instances for stats", "error", err)
		return nil
	}

	proc, err := procfs.NewFS(m.procDir)
	if err != nil {
		m.logger.Warn("cannot read stats", "error", err)
		return nil
	}
	devices, err := proc.NetDev()
	if err != nil {
		m.logger.Warn("cannot read network device stats", "error", err)
		return nil
	}

	var stats []types.InstanceStats
	for _, inst := range instances {
		vmm := m.vmm(inst.ID)
		if vmm == nil {
			continue
		}

		s, err := m.readStats(proc, devices, inst, vmm.PID())

		// Once the VMM has exited its PID may be another process's, so
		// what was read cannot be trusted, nor is it missed.
		select {
		case <-vmm.Done():
			continue
		default:
		}
		if err != nil {
			m.logger.Warn("cannot read instance stats", "instance_id", inst.ID, "error", err)
			continue
		}

		stats = append(stats, s)
	}

	return stats
}

// readStats reads what the VMM at pid uses of the host for inst.
func (m *Manager) readStats(
	proc procfs.FS, devices procfs.NetDev, inst types.InstanceSpec, pid int,
) (types.InstanceStats, error) {
	rt, err := m.Runtime(inst)
	if err != nil {
		return types.InstanceStats{}, err
	}

	p, err := proc.Proc(pid)
	if err != nil {
		return types.InstanceStats{}, fmt.Errorf("read VMM process %d: %w", pid, err)
	}
	stat, err := p.Stat()
	if err != nil {
		return types.InstanceStats{}, fmt.Errorf("read VMM process %d: %w", pid, err)
	}
	io, err := p.IO()
	if err != nil {
		return types.InstanceStats{}, fmt.Errorf("read VMM process %d: %w", pid, err)
	}

	// The TAP device is the host's end of the guest's interface: what it
	// transmits, the guest receives, and what it fails to transmit, the
	// guest never receives.
	tap := devices[network.TAPName(inst.ID)]

	return types.InstanceStats{
		InstanceID:             inst.ID,
		Name:                   inst.Name,
		StartedAt:              rt.StartedAt,
		ReadAt:                 time.Now(),
		Committed:              rt.Held(),
		CPUTime:                time.Duration(stat.CPUTime() * float64(time.Second)),
		ResidentMemoryBytes:    int64(stat.ResidentMemory()),
		DiskReadBytes:          int64(io.ReadBytes),
		DiskWrittenBytes:       int64(io.WriteBytes),
		NetworkReceiveBytes:    int64(tap.TxBytes),
		NetworkTransmitBytes:   int64(tap.RxBytes),
		NetworkReceivePackets:  int64(tap.TxPackets),
		NetworkTransmitPackets: int64(tap.RxPackets),
		NetworkReceiveDrops:    int64(tap.TxDropped),
		NetworkTransmitDrops:   int64(tap.RxDropped),
		NetworkReceiveErrors:   int64(tap.TxErrors),
		NetworkTransmitErrors:  int64(tap.RxErrors),
	}, nil
}
