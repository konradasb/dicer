// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"fmt"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/hypervisor"
)

// resolveStarter picks the hypervisor implementation an instance runs on at
// the given version, defaulting to the newest available when none is pinned.
func (m *Manager) resolveStarter(inst dicer.InstanceSpec, version string) (hypervisor.Starter, error) {
	hvType := inst.Hypervisor()

	starters := m.starters[hvType]
	if len(starters) == 0 {
		return nil, fmt.Errorf("hypervisor %s: %w", hvType, errors.ErrUnsupported)
	}

	if version == "" {
		return starters[0], nil
	}

	for _, s := range starters {
		if s.Version() == version {
			return s, nil
		}
	}

	return nil, dicer.InvalidArgument("no hypervisor %s %s on this host", hvType, version)
}

// snapshotStarter returns the hypervisor that took a snapshot. Restoring with
// another version is not supported by either hypervisor, so a missing version
// is an error rather than something to work around.
func (m *Manager) snapshotStarter(snap dicer.Snapshot) (hypervisor.Starter, error) {
	for _, s := range m.starters[snap.HypervisorType] {
		if s.Version() == snap.HypervisorVersion {
			return s, nil
		}
	}

	return nil, dicer.InvalidState("snapshot %q was taken with %s %s, which this daemon does not have",
		snap.Name, snap.HypervisorType, snap.HypervisorVersion)
}

// connect returns a control client for an instance's running VMM.
func (m *Manager) connect(inst dicer.InstanceSpec, rt dicer.InstanceStatus) (hypervisor.Hypervisor, error) {
	starter, err := m.resolveStarter(inst, rt.HypervisorVersion)
	if err != nil {
		return nil, err
	}

	hv, err := starter.Connect(rt.HypervisorSocketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to hypervisor: %w", err)
	}

	return hv, nil
}

// requireCapability fails with errors.ErrUnsupported unless the instance's
// hypervisor supports what an operation needs. feature names it for the
// error, as in "hypervisor firecracker does not support <feature>".
func requireCapability(inst dicer.InstanceSpec, supported bool, feature string) error {
	if supported {
		return nil
	}

	return fmt.Errorf("hypervisor %s does not support %s: %w",
		inst.Hypervisor(), feature, errors.ErrUnsupported)
}
