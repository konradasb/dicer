// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// Rename changes a Stopped or Failed instance's name and returns the renamed
// instance. Its persistent directory moves; everything keyed by ID stays.
func (m *Manager) Rename(ctx context.Context, inst types.InstanceSpec, newName string) (_ types.InstanceSpec, err error) {
	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	current, err := m.definitions.GetInstance(inst.ID)
	if err != nil {
		return types.InstanceSpec{}, err
	}

	rt, err := m.Runtime(current)
	if err != nil {
		return types.InstanceSpec{}, err
	}
	if !renameable(rt.State) {
		return types.InstanceSpec{}, errdefs.InvalidState(
			"instance %q is %s; rename it once it has stopped", current.Name, rt.State.Lower())
	}

	if newName == current.Name {
		return current, nil
	}

	renamed := current
	renamed.Name = newName

	if err := m.definitions.RenameInstance(current.ID, renamed); err != nil {
		return types.InstanceSpec{}, fmt.Errorf("rename instance %q: %w", current.Name, err)
	}

	m.record(renamed, types.ActionRenamed, fmt.Sprintf("Renamed instance %s to %s", current.Name, newName),
		map[string]string{"previous_name": current.Name})
	m.logger.InfoContext(ctx, "renamed instance", "instance", newName, "previous_name", current.Name)

	return renamed, nil
}

// renameable reports whether an instance in the state can be renamed.
func renameable(state types.InstanceState) bool {
	return state == types.StateStopped || state == types.StateFailed
}
