// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/konradasb/dicer/internal/diskfile"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/types"
)

// ForkSnapshot creates instance as a copy of the one snapshot was taken of,
// under an identity of its own: the caller defines it from
// snapshot.Instance with its own ID, name and address. From a memory
// snapshot the copy runs, resumed where the snapshot's guest was and then
// given that identity; from a disk snapshot it is stopped, to boot from the
// snapshot's disk. A fork that fails leaves no instance behind.
func (m *Manager) ForkSnapshot(ctx context.Context, snapshot types.Snapshot, instance types.InstanceSpec) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationForkSnapshot, started, err) }()

	from := fmt.Sprintf("%s snapshot %q of instance %s", snapshot.Kind, snapshot.Name, snapshot.Instance.Name)
	return m.fork(ctx, m.frozenSnapshot(snapshot), instance, from, map[string]string{"snapshot": snapshot.Name})
}

// ForkInstance creates instance as a copy of source. It is ForkSnapshot of a
// snapshot of source taken now, except that the snapshot is not kept. The
// caller defines instance from source. See writeSnapshot for the states
// source can be forked in.
func (m *Manager) ForkInstance(ctx context.Context, source, instance types.InstanceSpec) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationForkInstance, started, err) }()

	staged, err := m.definitions.StageSnapshot()
	if err != nil {
		return err
	}
	// A guest resumed from it keeps open the files it still reads, so they
	// can be removed.
	defer func() { _ = os.RemoveAll(staged) }()

	snapshot, paused, err := m.writeSnapshot(ctx, source, staged)
	if err != nil {
		return err
	}

	if paused > 0 {
		m.logger.InfoContext(ctx, "paused instance to fork it", "instance", source.Name, "paused_seconds", paused.Seconds())
	}

	frozen := frozenGuest{snapshot: snapshot, dir: staged, overlay: filepath.Join(staged, overlayDiskFile)}
	return m.fork(ctx, frozen, instance, "instance "+source.Name, map[string]string{"source_instance": source.Name})
}

// fork creates instance from a frozen guest, as ForkSnapshot describes. from
// says what the instance is a copy of in its events, and attrs are added to
// their attributes.
func (m *Manager) fork(
	ctx context.Context, frozen frozenGuest, instance types.InstanceSpec, from string, attrs map[string]string,
) (err error) {
	started := time.Now()

	if err := m.definitions.CreateInstance(instance); err != nil {
		return err
	}
	created := maps.Clone(attrs)
	created["image"] = instance.ImageRef
	m.record(instance, events.ActionCreated, "Forked instance from "+from, created)
	defer func() {
		if err == nil {
			return
		}
		if err := m.Delete(context.WithoutCancel(ctx), instance, true); err != nil {
			m.logger.ErrorContext(ctx, "cannot delete a fork that failed", "instance", instance.Name, "error", err)
		}
	}()

	if frozen.snapshot.Kind == types.SnapshotKindDisk {
		if err := diskfile.Copy(frozen.overlay, m.overlayDiskPath(instance)); err != nil {
			return fmt.Errorf("copy overlay disk: %w", err)
		}
		m.logger.InfoContext(ctx, "forked instance", "instance", instance.Name, "from", from)
		return nil
	}

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	if err := m.resume(ctx, instance, frozen); err != nil {
		return err
	}

	allocation, err := m.Allocation(instance)
	if err != nil {
		return err
	}
	attrs = maps.Clone(attrs)
	attrs["ip"] = allocation.IP
	m.record(instance, events.ActionStarted,
		fmt.Sprintf("Started instance from %s in %s, as itself: IP %s", from, humanize.Duration(time.Since(started)), allocation.IP),
		attrs)
	m.logger.InfoContext(ctx, "forked instance", "instance", instance.Name, "from", from, "ip", allocation.IP)

	return nil
}
