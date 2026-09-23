// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestSnapshotRestoresMemory freezes a running guest and puts it back.
//
// The marker is written to a tmpfs, so it exists only in the guest's RAM. A
// cold boot would lose both the mount and the file, which makes reading it
// back the difference between "the VM restarted" and "the VM was restored".
func TestSnapshotRestoresMemory(t *testing.T) {
	var (
		name     = instanceName(t)
		snapshot = "before-change"
		marker   = "only-in-memory"
	)

	env.createInstance(t, name)
	env.startInstance(t, name)

	env.exec(t, name, "sh", "-c",
		"mkdir -p /mnt/mem && mount -t tmpfs none /mnt/mem && echo "+marker+" > /mnt/mem/marker")

	env.dicer(t, "instance", "snapshot", "create", name, snapshot)

	snapshots := env.snapshots(t, name)
	if len(snapshots) != 1 || snapshots[0].Name != snapshot {
		t.Fatalf("snapshots = %+v, want one called %q", snapshots, snapshot)
	}

	// Taking a snapshot pauses the guest and resumes it; it does not stop
	// it. Restoring does require a stopped instance.
	if running := env.instance(t, name); running.State != "Running" {
		t.Errorf("instance is %q after snapshotting, want %q", running.State, "Running")
	}

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")

	env.dicer(t, "instance", "snapshot", "restore", name, snapshot)
	env.waitForState(t, name, "Running")

	out := env.exec(t, name, "cat", "/mnt/mem/marker")
	if got := strings.TrimSpace(out); got != marker {
		t.Errorf("the restored guest reads %q from its tmpfs, want %q: memory was not restored", got, marker)
	}
}

// TestSnapshotsBelongToTheirInstance checks that deleting an instance takes
// its snapshots with it, which is what makes them safe to leave lying around.
func TestSnapshotsBelongToTheirInstance(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	env.startInstance(t, name)

	env.dicer(t, "instance", "snapshot", "create", name, "doomed")
	env.dicer(t, "instance", "delete", name, "--force")

	// The snapshot went with the instance, so asking for it is a lookup
	// failure rather than an empty list.
	if _, err := env.tryDicer(t, "instance", "snapshot", "list", name); err == nil {
		t.Error("snapshots are still listed after the instance was deleted")
	}
}

// snapshotView is a row of `dicer instance snapshot list --format json`.
type snapshotView struct {
	Name     string `json:"Name"`
	Instance string `json:"Instance"`
	Size     string `json:"Size"`
}

// snapshots lists an instance's snapshots.
func (e *environment) snapshots(t *testing.T, instance string) []snapshotView {
	t.Helper()

	out := e.dicer(t, "instance", "snapshot", "list", instance, "--format", "json")

	return rows[snapshotView](t, out, "snapshots of "+instance)
}
