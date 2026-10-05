// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/diskfile"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/types"
)

// Fork creates instance as a copy of the one snapshot was taken of, under
// an identity of its own: the caller defines it from snapshot.Instance with
// its own ID, name and address. From a memory snapshot the copy runs,
// resumed where the snapshot's guest was and then given that identity; from
// a disk snapshot it is stopped, to boot from the snapshot's disk. A fork
// that fails leaves no instance behind.
func (m *Manager) Fork(ctx context.Context, snapshot types.Snapshot, instance types.InstanceSpec) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationForkSnapshot, started, err) }()

	if err := m.definitions.CreateInstance(instance); err != nil {
		return err
	}
	m.record(instance, events.ActionCreated,
		fmt.Sprintf("Forked instance from %s snapshot %q of instance %s", snapshot.Kind, snapshot.Name, snapshot.Instance.Name),
		map[string]string{"image": instance.ImageRef, "snapshot": snapshot.Name})
	defer func() {
		if err == nil {
			return
		}
		if err := m.Delete(context.WithoutCancel(ctx), instance, true); err != nil {
			m.logger.ErrorContext(ctx, "cannot delete a fork that failed", "instance", instance.Name, "error", err)
		}
	}()

	if snapshot.Kind == types.SnapshotKindDisk {
		if err := diskfile.Copy(m.snapshotOverlayDiskPath(snapshot), m.overlayDiskPath(instance)); err != nil {
			return fmt.Errorf("copy overlay disk: %w", err)
		}
		m.logger.InfoContext(ctx, "forked snapshot", "snapshot", snapshot.Name, "instance", instance.Name)
		return nil
	}

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	if err := m.resume(ctx, instance, snapshot); err != nil {
		return err
	}

	allocation, err := m.Allocation(instance)
	if err != nil {
		return err
	}
	m.record(instance, events.ActionStarted,
		fmt.Sprintf("Started instance from snapshot %q of instance %s in %s, as itself: IP %s",
			snapshot.Name, snapshot.Instance.Name, humanize.Duration(time.Since(started)), allocation.IP),
		map[string]string{"ip": allocation.IP, "snapshot": snapshot.Name})
	m.logger.InfoContext(ctx, "forked snapshot",
		"snapshot", snapshot.Name, "instance", instance.Name, "ip", allocation.IP)

	return nil
}
