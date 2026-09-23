// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"os"
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

func TestRuntimeDefaultsToStopped(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	rt, err := mgr.readRuntime("id-web")
	if err != nil {
		t.Fatalf("readRuntime: %v", err)
	}
	if rt.State != types.StateStopped {
		t.Errorf("state = %s, want Stopped for an instance with no runtime file", rt.State)
	}
}

func TestRuntimeRoundTripAndClear(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	pid := 4242
	err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID:    "id-web",
		State:         types.StateRunning,
		HypervisorPID: &pid,
	})
	if err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}

	got, err := mgr.readRuntime("id-web")
	if err != nil {
		t.Fatalf("readRuntime: %v", err)
	}
	if got.State != types.StateRunning || got.HypervisorPID == nil || *got.HypervisorPID != pid {
		t.Errorf("runtime = %+v, want state Running and pid %d", got, pid)
	}

	if err := mgr.clearRuntime("id-web"); err != nil {
		t.Fatalf("clearRuntime: %v", err)
	}
	got, err = mgr.readRuntime("id-web")
	if err != nil {
		t.Fatalf("readRuntime after clear: %v", err)
	}
	if got.State != types.StateStopped {
		t.Errorf("state after clear = %s, want Stopped", got.State)
	}
}

// A runtime file that cannot be parsed means nothing is known about what is
// running, which is exactly the case recovery exists to clean up.
func TestCorruptRuntimeReadsAsFailed(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	if err := mgr.ensureRuntimeDir("id-web"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mgr.runtimeStatePath("id-web"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	rt, err := mgr.readRuntime("id-web")
	if err != nil {
		t.Fatalf("readRuntime: %v", err)
	}
	if rt.State != types.StateFailed {
		t.Errorf("state = %s, want Failed for a corrupt runtime file", rt.State)
	}
}
