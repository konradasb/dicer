// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"os"
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

func TestStatusDefaultsToStopped(t *testing.T) {
	manager, _, _ := newTestManager(t)

	status, err := manager.readStatus("id-web")
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if status.State != types.InstanceStateStopped {
		t.Errorf("state = %s, want Stopped for an instance with no status file", status.State)
	}
}

func TestStatusRoundTripAndRemoval(t *testing.T) {
	manager, _, _ := newTestManager(t)

	pid := 4242
	err := manager.writeStatus(types.InstanceStatus{
		InstanceID: "id-web",
		State:      types.InstanceStateRunning,
		VMMPID:     &pid,
	})
	if err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	got, err := manager.readStatus("id-web")
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if got.State != types.InstanceStateRunning || got.VMMPID == nil || *got.VMMPID != pid {
		t.Errorf("status = %+v, want state Running and pid %d", got, pid)
	}

	if err := manager.removeRuntimeDir("id-web"); err != nil {
		t.Fatalf("removeRuntimeDir: %v", err)
	}
	got, err = manager.readStatus("id-web")
	if err != nil {
		t.Fatalf("readStatus after removal: %v", err)
	}
	if got.State != types.InstanceStateStopped {
		t.Errorf("state after removal = %s, want Stopped", got.State)
	}
}

// A status file that cannot be parsed means nothing is known about what is
// running, which is exactly the case recovery exists to clean up.
func TestCorruptStatusReadsAsFailed(t *testing.T) {
	manager, _, _ := newTestManager(t)

	if err := manager.ensureRuntimeDir("id-web"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.statusPath("id-web"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := manager.readStatus("id-web")
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if status.State != types.InstanceStateFailed {
		t.Errorf("state = %s, want Failed for a corrupt status file", status.State)
	}
}
