// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/dicer-sh/dicer"
)

// Delete removes an instance and everything it owns: the running VM if any,
// its host network resources, its address allocation, its runtime state and
// its persistent directory including the overlay disk.
//
// A running instance is refused unless force is set, so that a typo cannot
// destroy a live VM. Mounted volumes are never touched -- they have their own
// lifecycle and outlive the instances that use them.
func (m *Manager) Delete(ctx context.Context, inst dicer.InstanceSpec, force bool) (err error) {
	started := time.Now()
	defer func() { m.observe(opDelete, started, err) }()

	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	rt, err := m.Runtime(inst)
	if err != nil {
		return err
	}

	if rt.State.IsActive() && !force {
		return dicer.InvalidState("instance %q is %s", inst.Name, rt.State.Lower())
	}
	m.cancelRestart(inst.ID)
	// From here the delete is seen through whatever becomes of the
	// request, as a stop is.
	ctx = context.WithoutCancel(ctx)
	// A forced delete does not wait on the workload, as docker rm -f
	// does not: the instance is going, and nothing it saves is kept.
	m.stopVMM(ctx, inst, rt, false)

	m.teardownNetwork(ctx, inst)

	if err := m.addresses.Release(inst.NetworkName, inst.ID); err != nil {
		m.logger.WarnContext(ctx, "failed to release address allocation",
			"instance", inst.Name, "error", err)
	}

	if err := m.clearRuntime(inst.ID); err != nil {
		m.logger.WarnContext(ctx, "failed to clear runtime state",
			"instance", inst.Name, "error", err)
	}

	// Removing the definition also removes its directory and the overlay
	// disk inside it.
	if err := m.definitions.DeleteInstance(inst.Name); err != nil {
		return fmt.Errorf("delete instance %q: %w", inst.Name, err)
	}

	m.record(inst, dicer.ActionDeleted,
		"Deleted instance: removed its definition, disks and snapshots; released its address on network "+inst.NetworkName, nil)
	m.logger.InfoContext(ctx, "deleted instance", "instance", inst.Name)
	return nil
}
