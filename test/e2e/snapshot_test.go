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

// TestForkRunsBesideItsSource checks that a memory snapshot's fork resumes
// what only the guest's memory held, under its own hostname and address,
// while the instance it is a copy of goes on running: each reaches the other
// at its own address.
func TestForkRunsBesideItsSource(t *testing.T) {
	for _, hypervisor := range snapshotHypervisors {
		t.Run(hypervisor, func(t *testing.T) {
			var (
				name     = instanceName(t)
				fork     = name + "-fork"
				snapshot = name + "-snap"
				marker   = "only-in-memory"
			)

			env.createInstance(t, name, "--hypervisor-type", hypervisor)
			t.Cleanup(func() { env.deleteInstance(t, fork) })
			source := env.startInstance(t, name)
			env.exec(t, name, "sh", "-c",
				"mkdir -p /mnt/mem && mount -t tmpfs none /mnt/mem && echo "+marker+" > /mnt/mem/marker")

			env.dicer(t, "snapshot", "create", name, snapshot)
			t.Cleanup(func() { env.deleteSnapshot(t, snapshot) })
			env.dicer(t, "snapshot", "fork", snapshot, fork)

			forked := env.waitForState(t, fork, "Running")
			if forked.IP == "" || forked.IP == source.IP {
				t.Fatalf("fork's IP = %q, want one of its own, not its source's %s", forked.IP, source.IP)
			}

			if got := strings.TrimSpace(env.exec(t, fork, "cat", "/mnt/mem/marker")); got != marker {
				t.Errorf("the fork reads %q from its tmpfs, want %q: memory was not copied", got, marker)
			}
			if got := strings.TrimSpace(env.exec(t, fork, "hostname")); got != fork {
				t.Errorf("the fork's hostname is %q, want %q", got, fork)
			}
			if out := env.exec(t, fork, "ip", "-4", "-o", "addr", "show", "eth0"); !strings.Contains(out, forked.IP+"/") ||
				strings.Contains(out, source.IP+"/") {
				t.Errorf("the fork's eth0 has\n%s\nwant %s alone", out, forked.IP)
			}

			for _, ping := range []struct{ from, to string }{{fork, source.IP}, {name, forked.IP}} {
				if out, err := env.tryExec(t, ping.from, "ping", "-c", "1", "-W", "2", ping.to); err != nil {
					t.Errorf("%s cannot reach %s: %v\n%s", ping.from, ping.to, err, out)
				}
			}
		})
	}
}

// TestForkOfDiskSnapshotBootsFromItsDisk checks that a stopped instance's
// disk snapshot forks into a stopped instance that boots from that disk.
func TestForkOfDiskSnapshotBootsFromItsDisk(t *testing.T) {
	var (
		name     = instanceName(t)
		fork     = name + "-fork"
		snapshot = name + "-snap"
	)

	env.createInstance(t, name)
	t.Cleanup(func() { env.deleteInstance(t, fork) })
	env.startInstance(t, name)
	env.exec(t, name, "sh", "-c", "echo on-disk > /root/marker && sync")
	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")

	env.dicer(t, "snapshot", "create", name, snapshot)
	t.Cleanup(func() { env.deleteSnapshot(t, snapshot) })
	env.dicer(t, "snapshot", "fork", snapshot, fork)
	if state := env.instance(t, fork).State; state != "Stopped" {
		t.Errorf("a disk snapshot's fork is %q, want Stopped", state)
	}

	env.startInstance(t, fork)
	if got := strings.TrimSpace(env.exec(t, fork, "cat", "/root/marker")); got != "on-disk" {
		t.Errorf("the fork reads %q from its disk, want %q", got, "on-disk")
	}
}

// TestForkOfRunningInstanceKeepsNoSnapshot checks that forking a running
// instance resumes what only its guest's memory held, under the fork's own
// hostname and address, and keeps no snapshot.
func TestForkOfRunningInstanceKeepsNoSnapshot(t *testing.T) {
	for _, hypervisor := range snapshotHypervisors {
		t.Run(hypervisor, func(t *testing.T) {
			var (
				name   = instanceName(t)
				fork   = name + "-fork"
				marker = "only-in-memory"
			)

			env.createInstance(t, name, "--hypervisor-type", hypervisor)
			t.Cleanup(func() { env.deleteInstance(t, fork) })
			source := env.startInstance(t, name)
			env.exec(t, name, "sh", "-c",
				"mkdir -p /mnt/mem && mount -t tmpfs none /mnt/mem && echo "+marker+" > /mnt/mem/marker")

			env.dicer(t, "instance", "fork", name, fork)

			forked := env.waitForState(t, fork, "Running")
			if forked.IP == "" || forked.IP == source.IP {
				t.Fatalf("fork's IP = %q, want one of its own, not its source's %s", forked.IP, source.IP)
			}
			if state := env.instance(t, name).State; state != "Running" {
				t.Errorf("the forked instance is %q, want Running", state)
			}
			if got := strings.TrimSpace(env.exec(t, fork, "cat", "/mnt/mem/marker")); got != marker {
				t.Errorf("the fork reads %q from its tmpfs, want %q: memory was not copied", got, marker)
			}
			if got := strings.TrimSpace(env.exec(t, fork, "hostname")); got != fork {
				t.Errorf("the fork's hostname is %q, want %q", got, fork)
			}
			if out := env.dicer(t, "snapshot", "list", "--instance", name); strings.Contains(out, name) {
				t.Errorf("forking kept a snapshot:\n%s", out)
			}
		})
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
