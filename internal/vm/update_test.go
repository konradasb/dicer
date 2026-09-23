// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
)

func TestUpdateReleasesTheAddressOfAMovedInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := h.mgr.Address(h.inst); err != nil {
		t.Fatalf("a stopped instance keeps its address: %v", err)
	}

	moved := h.inst
	moved.StaticIP = "10.0.0.200"
	if err := h.mgr.Update(t.Context(), moved); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := h.mgr.Address(h.inst); !errors.Is(err, dicer.ErrNotFound) {
		t.Errorf("Address = %v, want the old address released for the new static IP", err)
	}
}

func TestUpdateRefusesARunningInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	changed := h.inst
	changed.VCPUs++
	if err := h.mgr.Update(t.Context(), changed); !errors.Is(err, dicer.ErrInvalidState) {
		t.Fatalf("Update = %v, want a refusal while the instance runs", err)
	}
}

// A restart policy is read when the instance ends, so changing it alone is
// fine while it runs -- and takes effect for the next end.
func TestUpdateChangesTheRestartPolicyOfARunningInstance(t *testing.T) {
	h := newHarness(t)
	h.restartAtOnce()
	h.start(t)

	changed, err := h.definitions.GetInstance(h.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed.Restart = dicer.RestartPolicy{Mode: dicer.RestartAlways}
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
	if err := h.mgr.Update(t.Context(), changed); err != nil {
		t.Fatalf("Update = %v, want the restart policy changed", err)
	}

	h.crash(t)
	h.waitForVMMs(t, 2)
	h.waitForState(t, dicer.StateRunning)
}
