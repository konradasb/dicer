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
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/guest"
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
	ctx context.Context, inst types.InstanceSpec, name string,
) (_ types.Snapshot, err error) {
	started := time.Now()
	defer func() { m.observe(opCreateSnapshot, started, err) }()

	if name == "" {
		name = snapshotName(time.Now())
	}
	if err := naming.Validate(name); err != nil {
		return types.Snapshot{}, err
	}

	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	rt, err := m.Runtime(inst)
	if err != nil {
		return types.Snapshot{}, err
	}
	if !rt.State.IsActive() {
		return types.Snapshot{}, errdefs.InvalidState("instance %q is %s; only a running or paused instance can be snapshotted",
			inst.Name, rt.State.Lower())
	}

	dir := m.snapshotDir(inst, name)
	if _, err := os.Stat(dir); err == nil {
		return types.Snapshot{}, errdefs.Exists("instance %q already has a snapshot %q", inst.Name, name)
	}

	hv, err := m.connect(inst, rt)
	if err != nil {
		return types.Snapshot{}, err
	}
	if err := requireCapability(inst, hv.Capabilities().SupportsSnapshot, "snapshots"); err != nil {
		return types.Snapshot{}, err
	}

	// Pause so memory and disk are consistent.
	if rt.State == types.StateRunning {
		if err := hv.PauseVM(ctx); err != nil {
			return types.Snapshot{}, fmt.Errorf("pause instance: %w", err)
		}
		defer func() {
			if err := hv.ResumeVM(context.WithoutCancel(ctx)); err != nil {
				m.logger.ErrorContext(ctx, "could not resume instance after snapshot",
					"instance", inst.Name, "error", err)
			}
		}()
	}

	snap, err := m.writeSnapshot(ctx, inst, rt, hv, name)
	if err != nil {
		_ = os.RemoveAll(dir)
		return types.Snapshot{}, err
	}

	m.record(inst, types.ActionSnapshotCreated, fmt.Sprintf("Created snapshot %q of memory and disk in %s: %s", name, duration(time.Since(started)), size(snap.SizeBytes)),
		map[string]string{"snapshot": name, "size_bytes": strconv.FormatInt(snap.SizeBytes, 10)})
	m.logger.InfoContext(ctx, "created snapshot",
		"instance", inst.Name, "snapshot", name, "size_bytes", snap.SizeBytes)

	return snap, nil
}

// writeSnapshot writes the hypervisor's state, the overlay disk and the
// metadata that ties them together.
func (m *Manager) writeSnapshot(
	ctx context.Context, inst types.InstanceSpec, rt types.InstanceStatus, hv hypervisor.Hypervisor, name string,
) (types.Snapshot, error) {
	dir := m.snapshotDir(inst, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return types.Snapshot{}, fmt.Errorf("create snapshot directory: %w", err)
	}

	if err := hv.SnapshotVM(ctx, dir); err != nil {
		return types.Snapshot{}, fmt.Errorf("snapshot vm: %w", err)
	}

	if err := copyDisk(m.overlayDiskPath(inst), m.snapshotOverlayDiskPath(inst, name)); err != nil {
		return types.Snapshot{}, fmt.Errorf("copy overlay disk: %w", err)
	}

	snap := types.Snapshot{
		Name:              name,
		InstanceID:        inst.ID,
		HypervisorType:    inst.Hypervisor(),
		HypervisorVersion: rt.HypervisorVersion,
		VCPUs:             rt.VCPUs,
		MemoryBytes:       rt.MemoryBytes,
		ImageDigest:       rt.ImageDigest,
		CreatedAt:         time.Now(),
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return types.Snapshot{}, fmt.Errorf("marshal snapshot metadata: %w", err)
	}
	if err := atomicfile.Write(m.snapshotMetadataPath(inst, name), data, 0o600); err != nil {
		return types.Snapshot{}, err
	}

	snap.SizeBytes, _ = dirSize(dir)

	return snap, nil
}

// ListSnapshots returns an instance's snapshots, oldest first.
func (m *Manager) ListSnapshots(inst types.InstanceSpec) ([]types.Snapshot, error) {
	dir := m.snapshotsDir(inst)

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

		snap, err := m.GetSnapshot(inst, e.Name())
		if err != nil {
			m.logger.Warn("skipping unreadable snapshot",
				"instance", inst.Name, "snapshot", e.Name(), "error", err)
			continue
		}
		snapshots = append(snapshots, snap)
	}

	slices.SortFunc(snapshots, func(a, b types.Snapshot) int { return a.CreatedAt.Compare(b.CreatedAt) })

	return snapshots, nil
}

// GetSnapshot returns one snapshot of an instance.
func (m *Manager) GetSnapshot(inst types.InstanceSpec, name string) (types.Snapshot, error) {
	if err := naming.Validate(name); err != nil {
		return types.Snapshot{}, err
	}

	data, err := os.ReadFile(m.snapshotMetadataPath(inst, name))
	if errors.Is(err, fs.ErrNotExist) {
		return types.Snapshot{}, errdefs.NotFound("instance %q has no snapshot %q", inst.Name, name)
	}
	if err != nil {
		return types.Snapshot{}, err
	}

	var snap types.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return types.Snapshot{}, fmt.Errorf("parse snapshot %q: %w", name, err)
	}
	snap.SizeBytes, _ = dirSize(m.snapshotDir(inst, name))

	return snap, nil
}

// DeleteSnapshot removes a snapshot and everything in it.
func (m *Manager) DeleteSnapshot(ctx context.Context, inst types.InstanceSpec, name string) (err error) {
	started := time.Now()
	defer func() { m.observe(opDeleteSnapshot, started, err) }()

	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	if _, err := m.GetSnapshot(inst, name); err != nil {
		return err
	}

	dir := m.snapshotDir(inst, name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}

	m.record(inst, types.ActionSnapshotDeleted, fmt.Sprintf("Deleted snapshot %q", name), map[string]string{"snapshot": name})
	m.logger.InfoContext(ctx, "deleted snapshot", "instance", inst.Name, "snapshot", name)

	return nil
}

// RestoreSnapshot resumes a stopped instance from a snapshot's memory and
// disk, discarding its current overlay disk.
func (m *Manager) RestoreSnapshot(ctx context.Context, inst types.InstanceSpec, name string) (err error) {
	started := time.Now()
	defer func() { m.observe(opRestoreSnapshot, started, err) }()

	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	snap, err := m.GetSnapshot(inst, name)
	if err != nil {
		return err
	}

	rt, err := m.Runtime(inst)
	if err != nil {
		return err
	}
	if rt.State.IsActive() {
		return errdefs.InvalidState("instance %q is %s; stop it before restoring a snapshot",
			inst.Name, rt.State.Lower())
	}
	m.cancelRestart(inst.ID)

	starter, err := m.snapshotStarter(snap)
	if err != nil {
		return err
	}

	need := types.Resources{VCPUs: snap.VCPUs, MemoryBytes: snap.MemoryBytes}
	if err := m.admit(inst, need); err != nil {
		return err
	}
	m.setStoppedByUser(ctx, inst, false)
	defer func() {
		if err != nil {
			m.fail(inst.ID, err)
		}
	}()

	img, err := m.snapshotImage(ctx, inst, snap)
	if err != nil {
		return err
	}

	vmm, hv, undo, err := m.restore(ctx, inst, snap, starter, img)
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
		hypervisorVersion: snap.HypervisorVersion,
		held:              need,
		imageDigest:       snap.ImageDigest,
		healthCheck:       types.EffectiveHealthCheck(inst.HealthCheck, img.HealthCheck),
	}
	running, err := m.recordRunning(inst, vmm, run)
	if err != nil {
		return err
	}

	cu.Release()
	m.supervise(ctx, inst, vmm, running)

	m.record(inst, types.ActionSnapshotRestored, fmt.Sprintf("Restored instance from snapshot %q taken %s in %s: memory and disk rolled back",
		name, snap.CreatedAt.Local().Format(time.DateTime), duration(time.Since(started))),
		map[string]string{"snapshot": name})
	m.logger.InfoContext(ctx, "restored snapshot",
		"instance", inst.Name, "snapshot", name, "pid", vmm.PID())

	return nil
}

// restore recreates the disks and TAP device the snapshot's devices refer to,
// then restores the VMM. The returned function undoes all of it.
func (m *Manager) restore(
	ctx context.Context, inst types.InstanceSpec, snap types.Snapshot, starter hypervisor.Starter, img *types.Image,
) (*process.Process, hypervisor.Hypervisor, func(), error) {
	cu := cleanup.Make(func() {})
	defer cu.Clean()

	if err := m.prepareRuntimeDir(inst.ID); err != nil {
		return nil, nil, nil, err
	}
	cu.Add(func() { _ = m.clearRuntime(inst.ID) })

	if err := copyDisk(m.snapshotOverlayDiskPath(inst, snap.Name), m.overlayDiskPath(inst)); err != nil {
		return nil, nil, nil, fmt.Errorf("restore overlay disk: %w", err)
	}

	// The restored VMM keeps the disks it was snapshotted with; only the
	// config disk is written afresh.
	mounts, _, err := m.resolveMounts(inst)
	if err != nil {
		return nil, nil, nil, err
	}

	netSetup, err := m.setupNetwork(ctx, inst)
	if err != nil {
		return nil, nil, nil, err
	}
	cu.Add(netSetup.cleanup)

	// The restored guest has already booted once.
	if err := m.writeGuestDisks(ctx, inst, starter, img, mounts, netSetup, guest.Status{Boots: 1}); err != nil {
		return nil, nil, nil, err
	}

	console := hypervisor.ConsoleConfig{Path: m.serialLogPath(inst)}
	vmm, hv, err := starter.RestoreVM(ctx, m.hypervisorSocketPath(inst.ID), m.snapshotDir(inst, snap.Name), console)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("restore vm: %w", err)
	}
	cu.Add(vmm.Terminate)

	return vmm, hv, cu.Release(), nil
}

// snapshotImage returns the image a snapshot's guest booted from, pulling it
// by digest if needed.
func (m *Manager) snapshotImage(ctx context.Context, inst types.InstanceSpec, snap types.Snapshot) (*types.Image, error) {
	ref, err := reference.Parse(inst.ImageRef)
	if err != nil {
		return nil, fmt.Errorf("image %q: %w", inst.ImageRef, err)
	}
	pinned := ref.Repository() + "@" + snap.ImageDigest

	img, err := m.images.Get(pinned)
	if errors.Is(err, errdefs.ErrNotFound) {
		img, err = m.images.Pull(ctx, pinned, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("get image %q: %w", pinned, err)
	}
	return img, nil
}

// snapshotName generates a snapshot name from t.
func snapshotName(t time.Time) string {
	return strings.ToLower(t.UTC().Format("20060102t150405z"))
}
