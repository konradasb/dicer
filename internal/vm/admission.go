// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"slices"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// Admission keeps the host from committing more CPU and memory than it
// allows, and instances from sharing a host port or a writable volume. What
// is committed is summed from the status of instances that hold resources,
// under admissionMu.

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

// admit records instance as Starting, holding need, if the host has room
// (ErrResourceExhausted otherwise) and its ports and volumes are free
// (ErrInvalidState otherwise). The caller must hold the instance lock.
func (m *Manager) admit(instance types.InstanceSpec, need types.Resources) error {
	m.admissionMu.Lock()
	defer m.admissionMu.Unlock()

	// The instance may have been deleted while the caller waited for the
	// lock.
	if _, err := m.definitions.Instance(instance.ID); err != nil {
		return err
	}

	if err := m.checkRoom(instance, need); err != nil {
		return err
	}
	if err := m.checkPorts(instance); err != nil {
		return err
	}
	if err := m.checkVolumes(instance); err != nil {
		return err
	}

	return m.transitionWith(instance, types.InstanceStateStarting, func(status *types.InstanceStatus) {
		status.VCPUs = need.VCPUs
		status.MemoryBytes = need.MemoryBytes
	})
}

// reserve makes a running instance hold need in place of what it holds, if
// the host has room (ErrResourceExhausted otherwise). The caller must hold
// the instance lock.
func (m *Manager) reserve(instance types.InstanceSpec, need types.Resources) error {
	m.admissionMu.Lock()
	defer m.admissionMu.Unlock()

	if err := m.checkRoom(instance, need); err != nil {
		return err
	}
	return m.transitionWith(instance, types.InstanceStateRunning, func(status *types.InstanceStatus) {
		status.VCPUs = need.VCPUs
		status.MemoryBytes = need.MemoryBytes
	})
}

// checkRoom returns ErrResourceExhausted unless the host has room for
// instance to hold need beside what the others hold. The caller must hold
// admissionMu.
func (m *Manager) checkRoom(instance types.InstanceSpec, need types.Resources) error {
	if m.capacity.Unlimited() {
		return nil
	}

	allocated, err := m.allocated(instance.ID)
	if err != nil {
		return err
	}

	allocatable := m.capacity.Allocatable()
	if !allocated.Add(need).Fits(allocatable) {
		return errdefs.ResourceExhausted("instance %q needs %s, but %s of the %s this host allows is committed",
			instance.Name, need, allocated, allocatable)
	}
	return nil
}

// allocated returns what the instances other than excludeID hold.
func (m *Manager) allocated(excludeID string) (types.Resources, error) {
	instances := m.definitions.Instances()

	var total types.Resources
	for _, instance := range instances {
		if instance.ID == excludeID {
			continue
		}

		status, err := m.Status(instance)
		if err != nil {
			return types.Resources{}, err
		}
		total = total.Add(status.HeldResources())
	}

	return total, nil
}

// checkPorts refuses an instance that would publish a host port another
// instance holds. The caller must hold admissionMu.
func (m *Manager) checkPorts(instance types.InstanceSpec) error {
	if len(instance.Ports) == 0 {
		return nil
	}

	instances := m.definitions.Instances()

	for _, other := range instances {
		if other.ID == instance.ID || len(other.Ports) == 0 {
			continue
		}

		status, err := m.Status(other)
		if err != nil {
			return err
		}
		if !status.State.HoldsPortsAndVolumes() {
			continue
		}

		for _, p := range instance.Ports {
			if slices.ContainsFunc(other.Ports, p.Overlaps) {
				return errdefs.InvalidState("port %s is already published by instance %q, which is %s",
					p, other.Name, status.State.Lowercase())
			}
		}
	}

	return nil
}

// checkVolumes refuses an instance that would share a volume with another
// that holds it, unless both mount it read-only. The caller must hold admissionMu.
func (m *Manager) checkVolumes(instance types.InstanceSpec) error {
	if !slices.ContainsFunc(instance.Mounts, isVolume) {
		return nil
	}

	instances := m.definitions.Instances()

	for _, other := range instances {
		if other.ID == instance.ID || !slices.ContainsFunc(other.Mounts, isVolume) {
			continue
		}

		status, err := m.Status(other)
		if err != nil {
			return err
		}
		if !status.State.HoldsPortsAndVolumes() {
			continue
		}

		for _, mine := range instance.Mounts {
			if !isVolume(mine) {
				continue
			}
			theirs, ok := other.VolumeMount(mine.Source)
			if ok && (!mine.ReadOnly || !theirs.ReadOnly) {
				return errdefs.InvalidState("volume %q is attached to instance %q, which is %s, "+
					"and a volume can be shared only while every instance mounts it read-only",
					mine.Source, other.Name, status.State.Lowercase())
			}
		}
	}

	return nil
}

// isVolume reports whether a mount is of a volume.
func isVolume(mount types.Mount) bool { return mount.Type == types.MountTypeVolume }
