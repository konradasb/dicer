// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"github.com/dicer-sh/dicer"
)

// Admission keeps the host from committing more CPU and memory than it has.
//
// An instance holds resources while it is Starting, Running or Paused -- see
// dicer.InstanceState.HoldsResources -- and what it holds is recorded in its runtime state
// when it is admitted. What the host has committed is the sum over those
// instances, worked out when it is asked for rather than kept as a running
// total: a VMM that dies, a daemon that restarts, a VM adopted by recovery
// all change it, and a counter kept alongside the truth would drift from it.
//
// A start is admitted if what it needs fits in what is left. The check and the
// record that the instance now holds its resources are made under one
// host-wide lock, so two starts cannot both take the last of something.

// CheckResources reports whether an instance asking for r could ever start
// on this host, however idle it was. It is for rejecting a definition when
// it is written rather than at its first start.
//
// It also rejects more vCPUs than the host has CPUs. Overcommit is for
// sharing CPUs among instances; one instance with more vCPUs than there are
// CPUs only makes its own threads contend with each other.
func (m *Manager) CheckResources(r dicer.Resources) error {
	if m.capacity.Unlimited() {
		return nil
	}

	if r.VCPUs > m.capacity.Host.VCPUs {
		return dicer.InvalidArgument("%d vCPUs is more than the host's %d CPUs",
			r.VCPUs, m.capacity.Host.VCPUs)
	}
	if allocatable := m.capacity.Allocatable(); !r.Fits(allocatable) {
		return dicer.InvalidArgument("%s is more than this host can give instances in total (%s)",
			r, allocatable)
	}

	return nil
}

// admit records inst as Starting, holding need, if the host has room for it.
// Otherwise it refuses with dicer.ErrResourceExhausted and changes nothing.
// It also refuses, with dicer.ErrInvalidState, an instance that would
// publish a host port another instance holds, or attach a volume another
// instance holds in a way the two cannot share.
//
// The caller must hold the instance lock.
func (m *Manager) admit(inst dicer.InstanceSpec, need dicer.Resources) error {
	m.admissionMu.Lock()
	defer m.admissionMu.Unlock()

	// The caller looked the instance up before taking its lock, and it may
	// have been deleted while the caller waited for it.
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
			return dicer.ResourceExhausted("instance %q needs %s, but %s of the %s this host allows is committed",
				inst.Name, need, allocated, allocatable)
		}
	}

	// Ports and volumes are checked here too: this lock is what makes a
	// start that passes the checks see every start admitted before it.
	if err := m.checkPorts(inst); err != nil {
		return err
	}
	if err := m.checkVolumes(inst); err != nil {
		return err
	}

	return m.transitionWith(inst, dicer.StateStarting, func(rt *dicer.InstanceStatus) {
		rt.VCPUs = need.VCPUs
		rt.MemoryBytes = need.MemoryBytes
	})
}

// allocated returns what the instances other than excludeID hold.
func (m *Manager) allocated(excludeID string) (dicer.Resources, error) {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		return dicer.Resources{}, err
	}

	var total dicer.Resources
	for _, inst := range instances {
		if inst.ID == excludeID {
			continue
		}

		rt, err := m.Runtime(inst)
		if err != nil {
			return dicer.Resources{}, err
		}
		total = total.Add(rt.Held())
	}

	return total, nil
}
