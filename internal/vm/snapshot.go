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

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/atomicfile"
	"github.com/dicer-sh/dicer/internal/guest"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	"github.com/dicer-sh/dicer/internal/image/reference"
	"github.com/dicer-sh/dicer/internal/process"
)

// CreateSnapshot freezes a running or paused instance to disk. An empty
// name is filled in from the time it is taken.
//
// A running instance is paused for as long as the snapshot takes and resumed
// afterwards; a paused one is left paused. Either way the guest carries on
// from where it was, and can be put back there with RestoreSnapshot.
func (m *Manager) CreateSnapshot(
	ctx context.Context, inst dicer.InstanceSpec, name string,
) (_ dicer.Snapshot, err error) {
	started := time.Now()
	defer func() { m.observe(opCreateSnapshot, started, err) }()

	if name == "" {
		name = snapshotName(time.Now())
	}
	if err := dicer.ValidateName(name); err != nil {
		return dicer.Snapshot{}, err
	}

	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	rt, err := m.Runtime(inst)
	if err != nil {
		return dicer.Snapshot{}, err
	}
	if !rt.State.IsActive() {
		return dicer.Snapshot{}, dicer.InvalidState("instance %q is %s; only a running or paused instance can be snapshotted",
			inst.Name, rt.State.Lower())
	}

	dir := m.snapshotDir(inst, name)
	if _, err := os.Stat(dir); err == nil {
		return dicer.Snapshot{}, dicer.Exists("instance %q already has a snapshot %q", inst.Name, name)
	}

	hv, err := m.connect(inst, rt)
	if err != nil {
		return dicer.Snapshot{}, err
	}
	if err := requireCapability(inst, hv.Capabilities().SupportsSnapshot, "snapshots"); err != nil {
		return dicer.Snapshot{}, err
	}

	// The guest must not be writing to memory or disk while either is
	// copied. Pausing is what makes the two consistent with each other.
	if rt.State == dicer.StateRunning {
		if err := hv.PauseVM(ctx); err != nil {
			return dicer.Snapshot{}, fmt.Errorf("pause instance: %w", err)
		}
		defer func() {
			// Resuming is best effort: the snapshot is already taken, and
			// an instance left paused can be resumed by hand.
			if err := hv.ResumeVM(context.WithoutCancel(ctx)); err != nil {
				m.logger.ErrorContext(ctx, "could not resume instance after snapshot",
					"instance", inst.Name, "error", err)
			}
		}()
	}

	snap, err := m.writeSnapshot(ctx, inst, rt, hv, name)
	if err != nil {
		_ = os.RemoveAll(dir)
		return dicer.Snapshot{}, err
	}

	m.record(inst, dicer.ActionSnapshotCreated, fmt.Sprintf("Created snapshot %q of memory and disk in %s: %s", name, duration(time.Since(started)), size(snap.SizeBytes)),
		map[string]string{"snapshot": name, "size_bytes": strconv.FormatInt(snap.SizeBytes, 10)})
	m.logger.InfoContext(ctx, "created snapshot",
		"instance", inst.Name, "snapshot", name, "size_bytes", snap.SizeBytes)

	return snap, nil
}

// writeSnapshot writes the hypervisor's state, the overlay disk and the
// metadata that ties them together.
func (m *Manager) writeSnapshot(
	ctx context.Context, inst dicer.InstanceSpec, rt dicer.InstanceStatus, hv hypervisor.Hypervisor, name string,
) (dicer.Snapshot, error) {
	dir := m.snapshotDir(inst, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return dicer.Snapshot{}, fmt.Errorf("create snapshot directory: %w", err)
	}

	if err := hv.SnapshotVM(ctx, dir); err != nil {
		return dicer.Snapshot{}, fmt.Errorf("snapshot vm: %w", err)
	}

	if err := copyDisk(m.overlayDiskPath(inst), m.snapshotOverlayDiskPath(inst, name)); err != nil {
		return dicer.Snapshot{}, fmt.Errorf("copy overlay disk: %w", err)
	}

	snap := dicer.Snapshot{
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
		return dicer.Snapshot{}, fmt.Errorf("marshal snapshot metadata: %w", err)
	}
	if err := atomicfile.Write(m.snapshotMetadataPath(inst, name), data, 0o600); err != nil {
		return dicer.Snapshot{}, err
	}

	snap.SizeBytes, _ = dirSize(dir)

	return snap, nil
}

// ListSnapshots returns an instance's snapshots, oldest first.
func (m *Manager) ListSnapshots(inst dicer.InstanceSpec) ([]dicer.Snapshot, error) {
	dir := m.snapshotsDir(inst)

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	snapshots := make([]dicer.Snapshot, 0, len(entries))
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

	slices.SortFunc(snapshots, func(a, b dicer.Snapshot) int { return a.CreatedAt.Compare(b.CreatedAt) })

	return snapshots, nil
}

// GetSnapshot returns one snapshot of an instance.
func (m *Manager) GetSnapshot(inst dicer.InstanceSpec, name string) (dicer.Snapshot, error) {
	if err := dicer.ValidateName(name); err != nil {
		return dicer.Snapshot{}, err
	}

	data, err := os.ReadFile(m.snapshotMetadataPath(inst, name))
	if errors.Is(err, fs.ErrNotExist) {
		return dicer.Snapshot{}, dicer.NotFound("instance %q has no snapshot %q", inst.Name, name)
	}
	if err != nil {
		return dicer.Snapshot{}, err
	}

	var snap dicer.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return dicer.Snapshot{}, fmt.Errorf("parse snapshot %q: %w", name, err)
	}
	snap.SizeBytes, _ = dirSize(m.snapshotDir(inst, name))

	return snap, nil
}

// DeleteSnapshot removes a snapshot and everything in it.
func (m *Manager) DeleteSnapshot(ctx context.Context, inst dicer.InstanceSpec, name string) (err error) {
	started := time.Now()
	defer func() { m.observe(opDeleteSnapshot, started, err) }()

	// Held so that a snapshot cannot be removed from under a restore that
	// is reading it.
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

	m.record(inst, dicer.ActionSnapshotDeleted, fmt.Sprintf("Deleted snapshot %q", name), map[string]string{"snapshot": name})
	m.logger.InfoContext(ctx, "deleted snapshot", "instance", inst.Name, "snapshot", name)

	return nil
}

// RestoreSnapshot puts an instance back to the moment a snapshot was taken:
// the guest resumes from the memory it had then, over the disk it had then.
//
// The instance must be stopped, and its current overlay disk is replaced by
// the snapshot's -- restoring is going back in time, and anything written
// since is discarded. The hypervisor version that took the snapshot is the
// one used to restore it.
func (m *Manager) RestoreSnapshot(ctx context.Context, inst dicer.InstanceSpec, name string) (err error) {
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
		return dicer.InvalidState("instance %q is %s; stop it before restoring a snapshot",
			inst.Name, rt.State.Lower())
	}
	// Restoring is a start by a user, and takes over from a pending restart.
	m.cancelRestart(inst.ID)

	starter, err := m.snapshotStarter(snap)
	if err != nil {
		return err
	}

	need := dicer.Resources{VCPUs: snap.VCPUs, MemoryBytes: snap.MemoryBytes}
	if err := m.admit(inst, need); err != nil {
		return err
	}
	m.setStoppedByUser(ctx, inst, false)
	defer func() {
		if err != nil {
			m.fail(inst.ID, err)
		}
	}()

	// The image supplies the read-only root disk: the one the guest was
	// booted from, re-pulled if it is gone.
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

	// The hypervisors restore a guest paused, so that a caller can put the
	// host back in order before the guest runs again. It is in order.
	if err := hv.ResumeVM(ctx); err != nil {
		return fmt.Errorf("resume restored instance: %w", err)
	}

	run := runRecord{
		hypervisorVersion: snap.HypervisorVersion,
		held:              need,
		imageDigest:       snap.ImageDigest,
		healthCheck:       dicer.EffectiveHealthCheck(inst.HealthCheck, img.HealthCheck),
	}
	running, err := m.recordRunning(inst, vmm, run)
	if err != nil {
		return err
	}

	cu.Release()
	m.supervise(ctx, inst, vmm, running)

	m.record(inst, dicer.ActionSnapshotRestored, fmt.Sprintf("Restored instance from snapshot %q taken %s in %s: memory and disk rolled back",
		name, snap.CreatedAt.Local().Format(time.DateTime), duration(time.Since(started))),
		map[string]string{"snapshot": name})
	m.logger.InfoContext(ctx, "restored snapshot",
		"instance", inst.Name, "snapshot", name, "pid", vmm.PID())

	return nil
}

// restore rebuilds everything the snapshot expects to find on the host, then
// hands it to the hypervisor.
//
// The returned undo function takes all of it down again, the VMM included,
// for a caller that fails before the restored instance is recorded.
//
// A snapshot records the paths of the files its devices were backed by, and
// the hypervisor reopens every one of them. The runtime directory is a
// tmpfs, so after a reboot the config disk has to be built again; the TAP
// device likewise. Both are derived from the instance, so they come back
// identical.
func (m *Manager) restore(
	ctx context.Context, inst dicer.InstanceSpec, snap dicer.Snapshot, starter hypervisor.Starter, img *dicer.Image,
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

	files, err := m.resolveFiles(inst)
	if err != nil {
		return nil, nil, nil, err
	}

	netSetup, err := m.setupNetwork(ctx, inst)
	if err != nil {
		return nil, nil, nil, err
	}
	cu.Add(netSetup.cleanup)

	// The restored guest booted before it was snapshotted, so it starts out
	// having booted once: a reset from here on is a second boot.
	if err := m.writeGuestDisks(ctx, inst, starter, img, files, netSetup, guest.Status{Boots: 1}); err != nil {
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

// snapshotImage returns the image a snapshot's guest was booted from, by
// its digest: held on this host, or else pulled from the instance's
// repository.
func (m *Manager) snapshotImage(ctx context.Context, inst dicer.InstanceSpec, snap dicer.Snapshot) (*dicer.Image, error) {
	ref, err := reference.Parse(inst.ImageRef)
	if err != nil {
		return nil, fmt.Errorf("image %q: %w", inst.ImageRef, err)
	}
	pinned := ref.Repository() + "@" + snap.ImageDigest

	img, err := m.images.Get(pinned)
	if errors.Is(err, dicer.ErrNotFound) {
		img, err = m.images.Pull(ctx, pinned, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("get image %q: %w", pinned, err)
	}
	return img, nil
}

// snapshotName returns a name for a snapshot the caller did not name, from
// the time it is taken.
func snapshotName(t time.Time) string {
	return strings.ToLower(t.UTC().Format("20060102t150405z"))
}
