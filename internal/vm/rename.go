// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"

	"github.com/dicer-sh/dicer"
)

// Rename changes a stopped instance's name, and returns it as it now is.
//
// The instance keeps its ID, so nothing derived from that moves: its runtime
// directory, its TAP device and the address it holds on its network are all
// untouched. What moves is its persistent directory, which is keyed by name
// and holds its overlay disk, its console log and its snapshots.
//
// An instance that is running, or about to be, is refused: the name is where
// its files are while a VMM has them open, and the guest took its hostname
// from the old name when it booted, so a rename could not fully take effect
// until a restart anyway. One that has stopped can be renamed however it
// stopped -- a guest that exited non-zero leaves the instance Failed, which
// is every bit as stopped as Stopped.
func (m *Manager) Rename(ctx context.Context, inst dicer.InstanceSpec, newName string) (_ dicer.InstanceSpec, err error) {
	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	current, err := m.definitions.GetInstance(inst.ID)
	if err != nil {
		return dicer.InstanceSpec{}, err
	}

	rt, err := m.Runtime(current)
	if err != nil {
		return dicer.InstanceSpec{}, err
	}
	if !renameable(rt.State) {
		return dicer.InstanceSpec{}, dicer.InvalidState(
			"instance %q is %s; rename it once it has stopped", current.Name, rt.State.Lower())
	}

	if newName == current.Name {
		return current, nil
	}

	renamed := current
	renamed.Name = newName

	if err := m.definitions.RenameInstance(current.ID, renamed); err != nil {
		return dicer.InstanceSpec{}, fmt.Errorf("rename instance %q: %w", current.Name, err)
	}

	// The event is recorded against the new name, which is what the
	// instance is called from now on, and says what it was called before.
	m.record(renamed, dicer.ActionRenamed, fmt.Sprintf("Renamed instance %s to %s", current.Name, newName),
		map[string]string{"previous_name": current.Name})
	m.logger.InfoContext(ctx, "renamed instance", "instance", newName, "previous_name", current.Name)

	return renamed, nil
}

// renameable reports whether an instance in the state can be renamed: one
// that holds no VMM and is not on its way to holding one. Failed counts as
// stopped, because it is: a guest that exited non-zero has ended.
func renameable(state dicer.InstanceState) bool {
	return state == dicer.StateStopped || state == dicer.StateFailed
}
