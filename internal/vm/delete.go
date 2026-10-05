// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
)

// Delete removes an instance and everything it owns: its VM, network
// resources, address, status and directory. Volumes and snapshots are kept. An
// active instance is refused unless force is set.
func (m *Manager) Delete(ctx context.Context, instance types.InstanceSpec, force bool) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationDelete, started, err) }()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := m.Status(instance)
	if err != nil {
		return err
	}

	if status.State.IsActive() && !force {
		return errdefs.InvalidState("instance %q is %s", instance.Name, status.State.Lowercase())
	}
	m.cancelRestart(instance.ID)
	// Finish the delete even if the request is cancelled.
	ctx = context.WithoutCancel(ctx)
	m.stopVMM(ctx, instance, status, false)

	m.teardownNetwork(ctx, instance)

	if err := m.networks.Release(instance.NetworkName, instance.ID); err != nil {
		m.logger.WarnContext(ctx, "failed to release network allocation",
			"instance", instance.Name, "error", err)
	}

	if err := m.removeRuntimeDir(instance.ID); err != nil {
		m.logger.WarnContext(ctx, "failed to remove the runtime directory",
			"instance", instance.Name, "error", err)
	}

	if err := m.definitions.DeleteInstance(instance.Name); err != nil {
		return fmt.Errorf("delete instance %q: %w", instance.Name, err)
	}

	m.record(instance, events.ActionDeleted,
		"Deleted instance: removed its definition and disks; released its address on network "+instance.NetworkName, nil)
	m.logger.InfoContext(ctx, "deleted instance", "instance", instance.Name)
	return nil
}
