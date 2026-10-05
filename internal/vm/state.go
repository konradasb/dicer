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
func (m *Manager) transition(instance types.InstanceSpec, to types.InstanceState) error {
	return m.transitionWith(instance, to, nil)
}

// transitionWith is transition that also applies update in the same write.
func (m *Manager) transitionWith(instance types.InstanceSpec, to types.InstanceState, update func(*types.InstanceStatus)) error {
	status, err := m.Status(instance)
	if err != nil {
		return err
	}

	if status.State != to && !status.State.CanTransitionTo(to) {
		return errdefs.InvalidState("instance %q is %s, and cannot become %s",
			instance.Name, status.State.Lowercase(), to.Lowercase())
	}

	status.State = to
	status.StateError = ""
	if update != nil {
		update(&status)
	}

	return m.writeStatus(status)
}

// fail records an instance as Failed because of cause, clearing its process
// and held resources. Write errors are logged. The caller must hold the
// instance lock.
func (m *Manager) fail(instanceID string, cause error) {
	status, err := m.readStatus(instanceID)
	if err != nil {
		m.logger.Warn("cannot read instance status", "instance_id", instanceID, "error", err)
		status = types.InstanceStatus{InstanceID: instanceID}
	}

	forgetProcess(&status)
	status.State = types.InstanceStateFailed
	status.StateError = cause.Error()

	if err := m.writeStatus(status); err != nil {
		m.logger.Warn("cannot record failed state", "instance_id", instanceID, "error", err)
	}
}

// forgetProcess clears a status's VMM and held resources.
func forgetProcess(status *types.InstanceStatus) {
	status.VMMPID = nil
	status.HypervisorSocketPath = ""
	status.VCPUs = 0
	status.MemoryBytes = 0
	status.StartedAt = time.Time{}
	status.NextRestartAt = time.Time{}
}
