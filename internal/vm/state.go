// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// transition moves an instance to a new lifecycle state, rejecting moves the
// state machine does not allow. The caller must hold the instance lock.
func (m *Manager) transition(inst types.InstanceSpec, to types.InstanceState) error {
	return m.transitionWith(inst, to, nil)
}

// transitionWith is transition that also applies update in the same write.
func (m *Manager) transitionWith(inst types.InstanceSpec, to types.InstanceState, update func(*types.InstanceStatus)) error {
	rt, err := m.Runtime(inst)
	if err != nil {
		return err
	}

	if rt.State != to && !rt.State.CanTransitionTo(to) {
		return errdefs.InvalidState("instance %q is %s, and cannot become %s",
			inst.Name, rt.State.Lower(), to.Lower())
	}

	rt.State = to
	rt.StateError = ""
	if update != nil {
		update(&rt)
	}

	return m.writeRuntime(rt)
}

// fail records an instance as Failed because of cause, clearing its process
// and held resources. Write errors are logged. The caller must hold the
// instance lock.
func (m *Manager) fail(instanceID string, cause error) {
	rt, err := m.readRuntime(instanceID)
	if err != nil {
		m.logger.Warn("cannot read runtime state", "instance_id", instanceID, "error", err)
		rt = types.InstanceStatus{InstanceID: instanceID}
	}

	forgetProcess(&rt)
	rt.State = types.StateFailed
	rt.StateError = cause.Error()

	if err := m.writeRuntime(rt); err != nil {
		m.logger.Warn("cannot record failed state", "instance_id", instanceID, "error", err)
	}
}

// forgetProcess clears a runtime state's VMM and held resources.
func forgetProcess(rt *types.InstanceStatus) {
	rt.HypervisorPID = nil
	rt.HypervisorSocketPath = ""
	rt.VCPUs = 0
	rt.MemoryBytes = 0
	rt.StartedAt = time.Time{}
	rt.NextRestartAt = time.Time{}
}
