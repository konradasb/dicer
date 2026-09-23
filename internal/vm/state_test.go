// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

func TestStateTransitions(t *testing.T) {
	tests := []struct {
		from, to types.InstanceState
		want     bool
	}{
		{types.StateStopped, types.StateStarting, true},
		{types.StateStarting, types.StateRunning, true},
		{types.StateRunning, types.StatePaused, true},
		{types.StatePaused, types.StateRunning, true},
		{types.StateRunning, types.StateStopping, true},
		{types.StateStopping, types.StateStopped, true},
		{types.StateFailed, types.StateStarting, true},
		{types.StateFailed, types.StateStopping, true},
		// An instance that ends is restarted, or left stopped or failed.
		{types.StateRunning, types.StateRestarting, true},
		{types.StatePaused, types.StateRestarting, true},
		{types.StateRunning, types.StateStopped, true},
		{types.StateRestarting, types.StateStarting, true},
		{types.StateRestarting, types.StateStopping, true},
		{types.StateStarting, types.StateRestarting, true},
		// A stopped instance cannot jump straight to running.
		{types.StateStopped, types.StateRunning, false},
		{types.StateStopped, types.StatePaused, false},
		{types.StateRunning, types.StateStarting, false},
		// A restart does not skip starting.
		{types.StateRestarting, types.StateRunning, false},
	}

	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
			t.Errorf("%s -> %s = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestTransitionRejectsIllegalMove(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")

	// Stopped -> Paused is not a legal move; it must be refused before
	// anything touches the host.
	if err := mgr.transition(inst, types.StatePaused); !errors.Is(err, errdefs.ErrInvalidState) {
		t.Errorf("error = %v, want ErrInvalidState", err)
	}

	if err := mgr.transition(inst, types.StateStarting); err != nil {
		t.Errorf("Stopped -> Starting should be allowed: %v", err)
	}
}

// transition moves the state and nothing else: the recorded process details
// belong to whoever started the process.
func TestTransitionKeepsProcessDetails(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	if err := h.mgr.transition(h.inst, types.StatePaused); err != nil {
		t.Fatalf("transition: %v", err)
	}

	rt := h.runtime(t)
	if rt.State != types.StatePaused {
		t.Errorf("state = %s, want Paused", rt.State)
	}
	if rt.HypervisorPID == nil || rt.HypervisorVersion != testHypervisorVersion {
		t.Errorf("runtime = %+v, want the process details kept", rt)
	}
}

// fail records why, and forgets the process: there is no longer one.
func TestFailClearsProcessDetails(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	h.mgr.fail(h.inst.ID, errors.New("boom"))

	rt := h.runtime(t)
	if rt.State != types.StateFailed || rt.StateError != "boom" {
		t.Errorf("runtime = %+v, want Failed/boom", rt)
	}
	if rt.HypervisorPID != nil {
		t.Errorf("HypervisorPID = %d, want it cleared", *rt.HypervisorPID)
	}
}
