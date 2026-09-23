// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"

	"github.com/dicer-sh/dicer"
)

func TestStateTransitions(t *testing.T) {
	tests := []struct {
		from, to dicer.InstanceState
		want     bool
	}{
		{dicer.StateStopped, dicer.StateStarting, true},
		{dicer.StateStarting, dicer.StateRunning, true},
		{dicer.StateRunning, dicer.StatePaused, true},
		{dicer.StatePaused, dicer.StateRunning, true},
		{dicer.StateRunning, dicer.StateStopping, true},
		{dicer.StateStopping, dicer.StateStopped, true},
		{dicer.StateFailed, dicer.StateStarting, true},
		{dicer.StateFailed, dicer.StateStopping, true},
		// An instance that ends is restarted, or left stopped or failed.
		{dicer.StateRunning, dicer.StateRestarting, true},
		{dicer.StatePaused, dicer.StateRestarting, true},
		{dicer.StateRunning, dicer.StateStopped, true},
		{dicer.StateRestarting, dicer.StateStarting, true},
		{dicer.StateRestarting, dicer.StateStopping, true},
		{dicer.StateStarting, dicer.StateRestarting, true},
		// A stopped instance cannot jump straight to running.
		{dicer.StateStopped, dicer.StateRunning, false},
		{dicer.StateStopped, dicer.StatePaused, false},
		{dicer.StateRunning, dicer.StateStarting, false},
		// A restart does not skip starting.
		{dicer.StateRestarting, dicer.StateRunning, false},
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
	if err := mgr.transition(inst, dicer.StatePaused); !errors.Is(err, dicer.ErrInvalidState) {
		t.Errorf("error = %v, want ErrInvalidState", err)
	}

	if err := mgr.transition(inst, dicer.StateStarting); err != nil {
		t.Errorf("Stopped -> Starting should be allowed: %v", err)
	}
}

// transition moves the state and nothing else: the recorded process details
// belong to whoever started the process.
func TestTransitionKeepsProcessDetails(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	if err := h.mgr.transition(h.inst, dicer.StatePaused); err != nil {
		t.Fatalf("transition: %v", err)
	}

	rt := h.runtime(t)
	if rt.State != dicer.StatePaused {
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
	if rt.State != dicer.StateFailed || rt.StateError != "boom" {
		t.Errorf("runtime = %+v, want Failed/boom", rt)
	}
	if rt.HypervisorPID != nil {
		t.Errorf("HypervisorPID = %d, want it cleared", *rt.HypervisorPID)
	}
}
