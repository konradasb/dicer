// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"slices"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// Admission keeps the host from committing more CPU and memory than it
// allows. What is committed is summed from the runtime state of instances
// that hold resources, under admissionMu.

// CheckResources reports whether an instance asking for r could ever start on
// this host. An instance may not have more vCPUs than the host has CPUs.
func (m *Manager) CheckResources(r types.Resources) error {
	if m.capacity.Unlimited() {
		return nil
	}

	if r.VCPUs > m.capacity.Host.VCPUs {
		return errdefs.InvalidArgument("%d vCPUs is more than the host's %d CPUs",
			r.VCPUs, m.capacity.Host.VCPUs)
	}
	if allocatable := m.capacity.Allocatable(); !r.Fits(allocatable) {
		return errdefs.InvalidArgument("%s is more than this host can give instances in total (%s)",
			r, allocatable)
	}

	return nil
}

// admit records inst as Starting, holding need, if the host has room
// (ErrResourceExhausted otherwise) and its ports and volumes are free
// (ErrInvalidState otherwise). The caller must hold the instance lock.
func (m *Manager) admit(inst types.InstanceSpec, need types.Resources) error {
	m.admissionMu.Lock()
	defer m.admissionMu.Unlock()

	// The instance may have been deleted while the caller waited for the
	// lock.
	if _, err := m.definitions.GetInstance(inst.ID); err != nil {
		return err
	}

	if !m.capacity.Unlimited() {
		allocated, err := m.allocated(inst.ID)
		if err != nil {
			return err
		}

		allocatable := m.capacity.Allocatable()
		if !allocated.Add(need).Fits(allocatable) {
			return errdefs.ResourceExhausted("instance %q needs %s, but %s of the %s this host allows is committed",
				inst.Name, need, allocated, allocatable)
		}
	}

	if err := m.checkPorts(inst); err != nil {
		return err
	}
	if err := m.checkVolumes(inst); err != nil {
		return err
	}

	return m.transitionWith(inst, types.StateStarting, func(rt *types.InstanceStatus) {
		rt.VCPUs = need.VCPUs
		rt.MemoryBytes = need.MemoryBytes
	})
}

// allocated returns what the instances other than excludeID hold.
func (m *Manager) allocated(excludeID string) (types.Resources, error) {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		return types.Resources{}, err
	}

	var total types.Resources
	for _, inst := range instances {
		if inst.ID == excludeID {
			continue
		}

		rt, err := m.Runtime(inst)
		if err != nil {
			return types.Resources{}, err
		}
		total = total.Add(rt.Held())
	}

	return total, nil
}

// checkVolumes refuses an instance that would share a volume with an active
// instance unless both mount it read-only. The caller must hold admissionMu.
func (m *Manager) checkVolumes(inst types.InstanceSpec) error {
	if !slices.ContainsFunc(inst.Mounts, isVolume) {
		return nil
	}

	instances, err := m.definitions.ListInstances()
	if err != nil {
		return err
	}

	for _, other := range instances {
		if other.ID == inst.ID || !slices.ContainsFunc(other.Mounts, isVolume) {
			continue
		}

		rt, err := m.Runtime(other)
		if err != nil {
			return err
		}
		if !rt.State.HoldsResources() && rt.State != types.StateStopping {
			continue
		}

		for _, mine := range inst.Mounts {
			if !isVolume(mine) {
				continue
			}
			theirs, ok := other.MountsVolume(mine.Source)
			if ok && (!mine.ReadOnly || !theirs.ReadOnly) {
				return errdefs.InvalidState("volume %q is attached to instance %q, which is %s, "+
					"and a volume can be shared only while every instance mounts it read-only",
					mine.Source, other.Name, rt.State.Lower())
			}
		}
	}

	return nil
}

func isVolume(m types.Mount) bool { return m.Type == types.MountVolume }
