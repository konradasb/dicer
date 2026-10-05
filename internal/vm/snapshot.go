// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gvisor.dev/gvisor/pkg/cleanup"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/diskfile"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// CreateSnapshot freezes a running or paused instance to disk. An empty name
// is generated from the time. A running instance is paused while the
// snapshot is taken.
func (m *Manager) CreateSnapshot(
	ctx context.Context, instance types.InstanceSpec, name string,
) (_ types.Snapshot, err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationCreateSnapshot, started, err) }()

	if name == "" {
		name = snapshotName(time.Now())
	}
	if err := naming.Validate(name); err != nil {
		return types.Snapshot{}, err
	}

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := m.Status(instance)
	if err != nil {
		return types.Snapshot{}, err
	}
	if !status.State.IsActive() {
		return types.Snapshot{}, errdefs.InvalidState("instance %q is %s; only a running or paused instance can be snapshotted",
			instance.Name, status.State.Lowercase())
	}

	dir := m.snapshotDir(instance, name)
	if _, err := os.Stat(dir); err == nil {
		return types.Snapshot{}, errdefs.Exists("instance %q already has a snapshot %q", instance.Name, name)
	}

	hv, err := m.connect(instance, status)
	if err != nil {
		return types.Snapshot{}, err
	}
	if err := requireCapability(instance, hv.Capabilities().SupportsSnapshot, "snapshots"); err != nil {
		return types.Snapshot{}, err
	}

	// Pause so memory and disk are consistent.
	if status.State == types.InstanceStateRunning {
		if err := hv.PauseVM(ctx); err != nil {
			return types.Snapshot{}, fmt.Errorf("pause instance: %w", err)
		}
		defer func() {
			if err := hv.ResumeVM(context.WithoutCancel(ctx)); err != nil {
				m.logger.ErrorContext(ctx, "could not resume instance after snapshot",
					"instance", instance.Name, "error", err)
			}
		}()
	}

	snapshot, err := m.writeSnapshot(ctx, instance, status, hv, name)
	if err != nil {
		_ = os.RemoveAll(dir)
		return types.Snapshot{}, err
	}

	m.record(instance, events.ActionSnapshotCreated, fmt.Sprintf("Created snapshot %q of memory and disk in %s: %s", name, humanize.Duration(time.Since(started)), humanize.Bytes(snapshot.SizeBytes)),
		map[string]string{"snapshot": name, "size_bytes": strconv.FormatInt(snapshot.SizeBytes, 10)})
	m.logger.InfoContext(ctx, "created snapshot",
		"instance", instance.Name, "snapshot", name, "size_bytes", snapshot.SizeBytes)

	return snapshot, nil
}

// writeSnapshot writes the hypervisor's state, the overlay disk and the
// metadata that ties them together.
func (m *Manager) writeSnapshot(
	ctx context.Context, instance types.InstanceSpec, status types.InstanceStatus, hv hypervisor.Hypervisor, name string,
) (types.Snapshot, error) {
	dir := m.snapshotDir(instance, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return types.Snapshot{}, fmt.Errorf("create snapshot directory: %w", err)
	}

	if err := hv.SnapshotVM(ctx, dir); err != nil {
		return types.Snapshot{}, fmt.Errorf("snapshot vm: %w", err)
	}

	if err := diskfile.Copy(m.overlayDiskPath(instance), m.snapshotOverlayDiskPath(instance, name)); err != nil {
		return types.Snapshot{}, fmt.Errorf("copy overlay disk: %w", err)
	}

	snapshot := types.Snapshot{
		Name:              name,
		InstanceID:        instance.ID,
		HypervisorType:    instance.EffectiveHypervisorType(),
		HypervisorVersion: status.HypervisorVersion,
		VCPUs:             status.VCPUs,
		MemoryBytes:       status.MemoryBytes,
		ImageDigest:       status.ImageDigest,
		CreatedAt:         time.Now(),
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return types.Snapshot{}, fmt.Errorf("marshal snapshot metadata: %w", err)
	}
	if err := atomicfile.Write(m.snapshotMetadataPath(instance, name), data, 0o600); err != nil {
		return types.Snapshot{}, err
	}

	snapshot.SizeBytes, _ = diskfile.AllocatedBytesUnder(dir)

	return snapshot, nil
}

// Snapshots returns an instance's snapshots, oldest first.
func (m *Manager) Snapshots(instance types.InstanceSpec) ([]types.Snapshot, error) {
	dir := m.snapshotsDir(instance)

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	snapshots := make([]types.Snapshot, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		snapshot, err := m.Snapshot(instance, e.Name())
		if err != nil {
			m.logger.Warn("skipping unreadable snapshot",
				"instance", instance.Name, "snapshot", e.Name(), "error", err)
			continue
		}
		snapshots = append(snapshots, snapshot)
	}

	slices.SortFunc(snapshots, func(a, b types.Snapshot) int { return a.CreatedAt.Compare(b.CreatedAt) })

	return snapshots, nil
}

// Snapshot returns an instance's snapshot by name.
func (m *Manager) Snapshot(instance types.InstanceSpec, name string) (types.Snapshot, error) {
	if err := naming.Validate(name); err != nil {
		return types.Snapshot{}, err
	}

	data, err := os.ReadFile(m.snapshotMetadataPath(instance, name))
	if errors.Is(err, fs.ErrNotExist) {
		return types.Snapshot{}, errdefs.NotFound("instance %q has no snapshot %q", instance.Name, name)
	}
	if err != nil {
		return types.Snapshot{}, err
	}

	var snapshot types.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return types.Snapshot{}, fmt.Errorf("parse snapshot %q: %w", name, err)
	}
	snapshot.SizeBytes, _ = diskfile.AllocatedBytesUnder(m.snapshotDir(instance, name))

	return snapshot, nil
}

// DeleteSnapshot removes a snapshot and everything in it.
func (m *Manager) DeleteSnapshot(ctx context.Context, instance types.InstanceSpec, name string) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationDeleteSnapshot, started, err) }()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	if _, err := m.Snapshot(instance, name); err != nil {
		return err
	}

	dir := m.snapshotDir(instance, name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}

	m.record(instance, events.ActionSnapshotDeleted, fmt.Sprintf("Deleted snapshot %q", name), map[string]string{"snapshot": name})
	m.logger.InfoContext(ctx, "deleted snapshot", "instance", instance.Name, "snapshot", name)

	return nil
}

// RestoreSnapshot resumes a stopped instance from a snapshot's memory and
// disk, discarding its current overlay disk.
func (m *Manager) RestoreSnapshot(ctx context.Context, instance types.InstanceSpec, name string) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationRestoreSnapshot, started, err) }()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	snapshot, err := m.Snapshot(instance, name)
	if err != nil {
		return err
	}

	status, err := m.Status(instance)
	if err != nil {
		return err
	}
	if status.State.IsActive() {
		return errdefs.InvalidState("instance %q is %s; stop it before restoring a snapshot",
			instance.Name, status.State.Lowercase())
	}
	m.cancelRestart(instance.ID)

	starter, err := m.snapshotStarter(snapshot)
	if err != nil {
		return err
	}

	need := types.Resources{VCPUs: snapshot.VCPUs, MemoryBytes: snapshot.MemoryBytes}
	if err := m.admit(instance, need); err != nil {
		return err
	}
	m.setStoppedByUser(ctx, instance, false)
	defer func() {
		if err != nil {
			m.fail(instance.ID, err)
		}
	}()

	image, err := m.snapshotImage(ctx, instance, snapshot)
	if err != nil {
		return err
	}

	vmm, hv, undo, err := m.restore(ctx, instance, snapshot, starter, image)
	if err != nil {
		return err
	}
	cu := cleanup.Make(undo)
	defer cu.Clean()

	// Hypervisors restore a guest paused.
	if err := hv.ResumeVM(ctx); err != nil {
		return fmt.Errorf("resume restored instance: %w", err)
	}

	run := runRecord{
		hypervisorVersion: snapshot.HypervisorVersion,
		held:              need,
		imageDigest:       snapshot.ImageDigest,
		healthCheck:       types.EffectiveHealthCheck(instance.HealthCheck, image.HealthCheck),
	}
	running, err := m.recordRunning(instance, vmm, run)
	if err != nil {
		return err
	}

	cu.Release()
	m.supervise(ctx, instance, vmm, running)

	m.record(instance, events.ActionSnapshotRestored, fmt.Sprintf("Restored instance from snapshot %q taken %s in %s: memory and disk rolled back",
		name, snapshot.CreatedAt.Local().Format(time.DateTime), humanize.Duration(time.Since(started))),
		map[string]string{"snapshot": name})
	m.logger.InfoContext(ctx, "restored snapshot",
		"instance", instance.Name, "snapshot", name, "pid", vmm.PID())

	return nil
}

// restore recreates the disks and TAP device the snapshot's devices refer to,
// then restores the VMM. The returned function undoes all of it.
func (m *Manager) restore(
	ctx context.Context, instance types.InstanceSpec, snapshot types.Snapshot, starter hypervisor.Starter, image *types.Image,
) (*process.Process, hypervisor.Hypervisor, func(), error) {
	cu := cleanup.Make(func() {})
	defer cu.Clean()

	if err := m.prepareRuntimeDir(instance.ID); err != nil {
		return nil, nil, nil, err
	}
	cu.Add(func() { _ = m.removeRuntimeDir(instance.ID) })

	if err := diskfile.Copy(m.snapshotOverlayDiskPath(instance, snapshot.Name), m.overlayDiskPath(instance)); err != nil {
		return nil, nil, nil, fmt.Errorf("restore overlay disk: %w", err)
	}

	// The restored VMM keeps the disks it was snapshotted with; only the
	// config disk is written afresh.
	mounts, _, err := m.resolveMounts(instance)
	if err != nil {
		return nil, nil, nil, err
	}

	setup, err := m.setupNetwork(ctx, instance)
	if err != nil {
		return nil, nil, nil, err
	}
	cu.Add(setup.cleanup)

	// The restored guest has already booted once.
	if err := m.writeGuestDisks(ctx, instance, starter, image, mounts, setup, guest.Status{Boots: 1}); err != nil {
		return nil, nil, nil, err
	}

	console := hypervisor.ConsoleConfig{Path: m.serialLogPath(instance)}
	vmm, hv, err := starter.RestoreVM(ctx, m.hypervisorSocketPath(instance.ID), m.snapshotDir(instance, snapshot.Name), console)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("restore vm: %w", err)
	}
	cu.Add(vmm.Terminate)

	return vmm, hv, cu.Release(), nil
}

// snapshotImage returns the image a snapshot's guest booted from, pulling it
// by digest if needed.
func (m *Manager) snapshotImage(ctx context.Context, instance types.InstanceSpec, snapshot types.Snapshot) (*types.Image, error) {
	ref, err := reference.Parse(instance.ImageRef)
	if err != nil {
		return nil, fmt.Errorf("image %q: %w", instance.ImageRef, err)
	}
	pinned := ref.Repository() + "@" + snapshot.ImageDigest

	image, err := m.images.Ensure(ctx, pinned, types.PullPolicyMissing)
	if err != nil {
		return nil, fmt.Errorf("get image %q: %w", pinned, err)
	}
	return image, nil
}

// snapshotName generates a snapshot name from t.
func snapshotName(t time.Time) string {
	return strings.ToLower(t.UTC().Format("20060102t150405z"))
}
