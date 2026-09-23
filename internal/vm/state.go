// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"time"

	"github.com/dicer-sh/dicer"
)

// transition moves an instance to a new lifecycle state, rejecting moves the
// state machine does not allow so that a bad request fails before it touches
// the host. Moving to the state it is already in is allowed.
//
// The caller must hold the instance lock.
func (m *Manager) transition(inst dicer.InstanceSpec, to dicer.InstanceState) error {
	return m.transitionWith(inst, to, nil)
}

// transitionWith is transition that also changes the runtime state as update
// says, in the same write.
func (m *Manager) transitionWith(inst dicer.InstanceSpec, to dicer.InstanceState, update func(*dicer.InstanceStatus)) error {
	rt, err := m.Runtime(inst)
	if err != nil {
		return err
	}

	if rt.State != to && !rt.State.CanTransitionTo(to) {
		return dicer.InvalidState("instance %q is %s, and cannot become %s",
			inst.Name, rt.State.Lower(), to.Lower())
	}

	rt.State = to
	rt.StateError = ""
	if update != nil {
		update(&rt)
	}

	return m.writeRuntime(rt)
}

// fail records that an instance is no longer running because of cause. The
// process details are cleared, since there is no longer a process, and so are
// the resources it held. It is best effort: it is called on paths that are
// already failing, and a write error is logged rather than allowed to mask
// the cause.
//
// The caller must hold the instance lock.
func (m *Manager) fail(instanceID string, cause error) {
	rt, err := m.readRuntime(instanceID)
	if err != nil {
		m.logger.Warn("cannot read runtime state", "instance_id", instanceID, "error", err)
		rt = dicer.InstanceStatus{InstanceID: instanceID}
	}

	forgetProcess(&rt)
	rt.State = dicer.StateFailed
	rt.StateError = cause.Error()

	if err := m.writeRuntime(rt); err != nil {
		m.logger.Warn("cannot record failed state", "instance_id", instanceID, "error", err)
	}
}

// forgetProcess clears what a runtime state says of a VMM that is gone, and
// of the resources it held.
func forgetProcess(rt *dicer.InstanceStatus) {
	rt.HypervisorPID = nil
	rt.HypervisorSocketPath = ""
	rt.VCPUs = 0
	rt.MemoryBytes = 0
	rt.StartedAt = time.Time{}
	rt.NextRestartAt = time.Time{}
}
