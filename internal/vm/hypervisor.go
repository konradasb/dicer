// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"fmt"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// resolveStarter returns the starter for an instance's hypervisor at version,
// or the default version if it is empty.
func (m *Manager) resolveStarter(inst types.InstanceSpec, version string) (hypervisor.Starter, error) {
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

	return nil, errdefs.InvalidArgument("no hypervisor %s %s on this host", hvType, version)
}

// snapshotStarter returns the hypervisor version that took a snapshot, which
// is the only one that can restore it.
func (m *Manager) snapshotStarter(snap types.Snapshot) (hypervisor.Starter, error) {
	for _, s := range m.starters[snap.HypervisorType] {
		if s.Version() == snap.HypervisorVersion {
			return s, nil
		}
	}

	return nil, errdefs.InvalidState("snapshot %q was taken with %s %s, which this daemon does not have",
		snap.Name, snap.HypervisorType, snap.HypervisorVersion)
}

// connect returns a control client for an instance's running VMM.
func (m *Manager) connect(inst types.InstanceSpec, rt types.InstanceStatus) (hypervisor.Hypervisor, error) {
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

// requireCapability returns errors.ErrUnsupported unless supported is true.
func requireCapability(inst types.InstanceSpec, supported bool, feature string) error {
	if supported {
		return nil
	}

	return fmt.Errorf("hypervisor %s does not support %s: %w",
		inst.Hypervisor(), feature, errors.ErrUnsupported)
}
