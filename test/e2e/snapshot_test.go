// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// snapshotHypervisors are the hypervisors snapshots are tested on: each
// writes and restores them its own way.
var snapshotHypervisors = []string{"cloud-hypervisor", "firecracker"}

// TestSnapshotRestoresMemory checks that a memory snapshot brings back what
// only the guest's memory held, through a rename of its instance: the
// restored hypervisor must find the instance's files where they are now.
func TestSnapshotRestoresMemory(t *testing.T) {
	for _, hypervisor := range snapshotHypervisors {
		t.Run(hypervisor, func(t *testing.T) {
			var (
				name     = instanceName(t)
				renamed  = name + "-renamed"
				snapshot = name + "-before"
				marker   = "only-in-memory"
			)

			env.createInstance(t, name, "--hypervisor-type", hypervisor)
			t.Cleanup(func() { env.deleteInstance(t, renamed) })
			env.startInstance(t, name)

			env.exec(t, name, "sh", "-c",
				"mkdir -p /mnt/mem && mount -t tmpfs none /mnt/mem && echo "+marker+" > /mnt/mem/marker")

			env.dicer(t, "snapshot", "create", name, snapshot)
			t.Cleanup(func() { env.deleteSnapshot(t, snapshot) })

			if got := env.snapshot(t, snapshot); got.Kind != "memory" || got.Instance != name {
				t.Errorf("snapshot = %+v, want a memory snapshot of %s", got, name)
			}

			// Taking a snapshot pauses the guest and resumes it; it does not
			// stop it.
			if running := env.instance(t, name); running.State != "Running" {
				t.Errorf("instance is %q after snapshotting, want %q", running.State, "Running")
			}

			env.dicer(t, "instance", "stop", name)
			env.waitForState(t, name, "Stopped")
			env.dicer(t, "rename", name, renamed)

			env.dicer(t, "snapshot", "restore", snapshot)
			env.waitForState(t, renamed, "Running")

			out := env.exec(t, renamed, "cat", "/mnt/mem/marker")
			if got := strings.TrimSpace(out); got != marker {
				t.Errorf("the restored guest reads %q from its tmpfs, want %q: memory was not restored", got, marker)
			}
		})
	}
}

// TestDiskSnapshotRollsBackTheDisk checks that a stopped instance's disk
// snapshot puts back what its disk held, for the guest to boot from.
func TestDiskSnapshotRollsBackTheDisk(t *testing.T) {
	var (
		name     = instanceName(t)
		snapshot = name + "-before"
	)

	env.createInstance(t, name)
	env.startInstance(t, name)
	env.exec(t, name, "sh", "-c", "echo before > /root/marker && sync")
	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")

	env.dicer(t, "snapshot", "create", name, snapshot)
	t.Cleanup(func() { env.deleteSnapshot(t, snapshot) })
	if got := env.snapshot(t, snapshot); got.Kind != "disk" {
		t.Errorf("snapshot of a stopped instance = %+v, want a disk snapshot", got)
	}

	env.startInstance(t, name)
	env.exec(t, name, "sh", "-c", "echo after > /root/marker && sync")
	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")

	env.dicer(t, "snapshot", "restore", snapshot)
	if state := env.instance(t, name).State; state != "Stopped" {
		t.Errorf("instance is %q after a disk restore, want it left Stopped", state)
	}

	env.startInstance(t, name)
	if got := strings.TrimSpace(env.exec(t, name, "cat", "/root/marker")); got != "before" {
		t.Errorf("the guest reads %q from its disk, want %q: the disk was not rolled back", got, "before")
	}
}

// TestSnapshotsOutliveTheirInstance checks that deleting an instance keeps
// its snapshots, and that they can then be deleted on their own.
func TestSnapshotsOutliveTheirInstance(t *testing.T) {
	var (
		name     = instanceName(t)
		snapshot = name + "-kept"
	)

	env.createInstance(t, name)
	env.startInstance(t, name)

	env.dicer(t, "snapshot", "create", name, snapshot)
	t.Cleanup(func() { env.deleteSnapshot(t, snapshot) })
	env.dicer(t, "instance", "delete", name, "--force")

	if got := env.snapshot(t, snapshot); got.Instance != name {
		t.Errorf("snapshot after its instance was deleted = %+v, want it to name %s", got, name)
	}
	if _, err := env.tryDicer(t, "snapshot", "restore", snapshot); err == nil {
		t.Error("a snapshot of a deleted instance was restored")
	}

	env.dicer(t, "snapshot", "delete", snapshot)
	if _, err := env.tryDicer(t, "snapshot", "show", snapshot); err == nil {
		t.Error("the snapshot is still there after it was deleted")
	}
}

// snapshotView is a row of `dicer snapshot list --format json`.
type snapshotView struct {
	Name     string `json:"Name"`
	Kind     string `json:"Kind"`
	Instance string `json:"Instance"`
	Size     string `json:"Size"`
}

// snapshot shows one snapshot.
func (e *environment) snapshot(t *testing.T, name string) snapshotView {
	t.Helper()

	out := e.dicer(t, "snapshot", "show", name, "--format", "json")

	views := rows[snapshotView](t, out, "snapshot "+name)
	if len(views) != 1 {
		t.Fatalf("snapshot show %s gave %d rows, want 1", name, len(views))
	}
	return views[0]
}

// deleteSnapshot removes a snapshot a test made, if it is still there.
func (e *environment) deleteSnapshot(t *testing.T, name string) {
	t.Helper()

	ctx, cancel := cleanupContext()
	defer cancel()

	if _, err := e.runDicer(ctx, "snapshot", "delete", name); err != nil && !isNotFound(err) {
		t.Logf("cleanup: delete snapshot %s: %v", name, err)
	}
}
