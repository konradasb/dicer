// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// Delete removes an instance and everything it owns: its VM, network
// resources, address, runtime state and directory. Volumes are kept. An
// active instance is refused unless force is set.
func (m *Manager) Delete(ctx context.Context, inst types.InstanceSpec, force bool) (err error) {
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
		return errdefs.InvalidState("instance %q is %s", inst.Name, rt.State.Lower())
	}
	m.cancelRestart(inst.ID)
	// Finish the delete even if the request is cancelled.
	ctx = context.WithoutCancel(ctx)
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

	if err := m.definitions.DeleteInstance(inst.Name); err != nil {
		return fmt.Errorf("delete instance %q: %w", inst.Name, err)
	}

	m.record(inst, types.ActionDeleted,
		"Deleted instance: removed its definition, disks and snapshots; released its address on network "+inst.NetworkName, nil)
	m.logger.InfoContext(ctx, "deleted instance", "instance", inst.Name)
	return nil
}
