// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/diskfile"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/types"
)

// standbyFile records, in an instance's standby directory, what its frozen
// guest ran with. The directory is whole once it is there.
const standbyFile = "standby.json"

// Standby freezes a running or paused instance to disk and ends its VMM, so
// that it holds no CPU or memory. Start resumes it where it was; Stop
// discards what was frozen. Its disk stays where it is, and its address, host
// ports and writable volumes stay its own.
func (m *Manager) Standby(ctx context.Context, instance types.InstanceSpec) error {
	return m.standby(ctx, instance, 0)
}

// standby is Standby. If idleFor is set, the instance has been idle that
// long, and is put on standby only if it is still running and its
// StandbyAfter, as defined now, has passed.
func (m *Manager) standby(ctx context.Context, instance types.InstanceSpec, idleFor time.Duration) (err error) {
	started := time.Now()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()
	defer m.syncWaker(ctx, instance.ID)

	status, err := m.Status(instance)
	if err != nil {
		return err
	}
	if idleFor > 0 {
		current, err := m.definitions.Instance(instance.ID)
		if err != nil || status.State != types.InstanceStateRunning ||
			current.StandbyAfter == 0 || idleFor < current.StandbyAfter {
			return nil
		}
	}
	defer func() { m.observeOperation(operationStandby, started, err) }()

	if !status.State.IsActive() {
		return errdefs.InvalidState("instance %q is %s; only a running or paused instance can be put on standby",
			instance.Name, status.State.Lowercase())
	}

	allocation, err := m.Allocation(instance)
	if err != nil {
		return err
	}
	hv, err := m.connect(instance, status)
	if err != nil {
		return err
	}
	if err := requireCapability(instance, hv.Capabilities().SupportsSnapshot, "standby"); err != nil {
		return err
	}

	// Written beside the standby directory and moved into place once
	// whole, after the VMM has ended: a crash before then loses the frozen
	// guest, as a crash of the VMM would, rather than leaving one the disk
	// may move on from.
	dir := m.standbyDir(instance)
	staged := dir + ".tmp"
	if err := os.RemoveAll(staged); err != nil {
		return err
	}
	if err := os.MkdirAll(staged, 0o700); err != nil {
		return fmt.Errorf("create standby directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staged) }()

	if status.State == types.InstanceStateRunning {
		if err := hv.PauseVM(ctx); err != nil {
			return fmt.Errorf("pause instance: %w", err)
		}
	}
	frozen := false
	defer func() {
		if !frozen && status.State == types.InstanceStateRunning {
			if err := hv.ResumeVM(context.WithoutCancel(ctx)); err != nil {
				m.logger.ErrorContext(ctx, "could not resume instance after a failed standby",
					"instance", instance.Name, "error", err)
			}
		}
	}()

	snapshotCtx, cancel := context.WithTimeout(ctx, memoryTransferTimeout(status.MemoryBytes))
	defer cancel()
	if err := hv.SnapshotVM(snapshotCtx, staged); err != nil {
		return fmt.Errorf("snapshot vm: %w", err)
	}

	standby := types.Snapshot{
		Name:              "standby",
		Kind:              types.SnapshotKindMemory,
		Instance:          instance,
		IP:                allocation.IP,
		MAC:               allocation.MAC,
		HypervisorType:    instance.EffectiveHypervisorType(),
		HypervisorVersion: status.HypervisorVersion,
		VCPUs:             status.VCPUs,
		MemoryBytes:       status.MemoryBytes,
		ImageDigest:       status.ImageDigest,
		CreatedAt:         time.Now(),
	}
	data, err := json.MarshalIndent(standby, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(staged, standbyFile), data, 0o600); err != nil {
		return err
	}

	// The guest is paused: there is nothing to flush, and nobody to ask to
	// shut down.
	frozen = true
	ending := status
	ending.State = types.InstanceStatePaused
	ctx = context.WithoutCancel(ctx)
	m.stopVMM(ctx, instance, ending, false)

	if err := os.Rename(staged, dir); err != nil {
		err = fmt.Errorf("move standby into place: %w", err)
		m.fail(instance.ID, err)
		return err
	}
	m.teardownNetwork(ctx, instance)
	if err := m.removeRuntimeDir(instance.ID); err != nil {
		return fmt.Errorf("remove runtime directory: %w", err)
	}

	size, _ := diskfile.AllocatedBytesUnder(dir)
	attrs := map[string]string{"size_bytes": strconv.FormatInt(size, 10)}
	why := ""
	if idleFor > 0 {
		attrs["idle_seconds"] = strconv.FormatInt(int64(idleFor.Seconds()), 10)
		why = " after " + humanize.Duration(idleFor) + " idle"
	}
	m.record(instance, events.ActionStandby, fmt.Sprintf("Put instance on standby%s, in %s: %s frozen to disk, %s memory released",
		why, humanize.Duration(time.Since(started)), humanize.Bytes(size), humanize.Bytes(status.MemoryBytes)), attrs)
	m.logger.InfoContext(ctx, "put instance on standby", "instance", instance.Name, "size_bytes", size)
	return nil
}

// resumeStandby resumes an instance on standby where it was, and discards
// what was frozen. wokenByPort is the published port a connection that woke
// it came to, or 0 for a start. The caller must hold the instance lock.
func (m *Manager) resumeStandby(ctx context.Context, instance types.InstanceSpec, wokenByPort uint16) error {
	started := time.Now()

	data, err := os.ReadFile(filepath.Join(m.standbyDir(instance), standbyFile))
	if err != nil {
		return fmt.Errorf("read standby: %w", err)
	}
	var standby types.Snapshot
	if err := json.Unmarshal(data, &standby); err != nil {
		return fmt.Errorf("parse standby: %w", err)
	}

	// Its ports are published again as it resumes. Connections that come
	// in the moment between are refused; those after reach its address,
	// and the guest once it is back.
	m.stopWaker(instance.ID)

	if err := m.resume(ctx, instance, frozenGuest{snapshot: standby, dir: m.standbyDir(instance)}); err != nil {
		m.record(instance, events.ActionDied, "Failed to resume instance from standby: "+err.Error(), nil)
		return err
	}
	// The VMM has what it reads of the frozen guest open, which outlives
	// its name.
	if err := os.RemoveAll(m.standbyDir(instance)); err != nil {
		m.logger.WarnContext(ctx, "cannot remove a resumed instance's standby", "instance", instance.Name, "error", err)
	}

	allocation, _ := m.Allocation(instance)
	attrs := map[string]string{"ip": allocation.IP}
	why := ""
	if wokenByPort != 0 {
		attrs["woken_by_port"] = strconv.Itoa(int(wokenByPort))
		why = fmt.Sprintf(", woken by a connection to port %d,", wokenByPort)
	}
	m.record(instance, events.ActionStarted, fmt.Sprintf("Resumed instance from standby%s in %s, where it was %s ago: IP %s",
		why, humanize.Duration(time.Since(started)), humanize.Duration(started.Sub(standby.CreatedAt)), allocation.IP), attrs)
	m.logger.InfoContext(ctx, "resumed instance from standby", "instance", instance.Name)
	return nil
}

// onStandby reports whether instance has a guest frozen on standby.
func (m *Manager) onStandby(instance types.InstanceSpec) bool {
	_, err := os.Stat(filepath.Join(m.standbyDir(instance), standbyFile))
	return err == nil
}

// discardStandby removes what an instance on standby has frozen, if it has
// anything.
func (m *Manager) discardStandby(instance types.InstanceSpec) error {
	if err := os.RemoveAll(m.standbyDir(instance)); err != nil {
		return fmt.Errorf("discard standby: %w", err)
	}
	return nil
}
