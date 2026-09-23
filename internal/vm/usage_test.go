// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"testing"

	"github.com/dicer-sh/dicer"
)

func TestUsageCountsEveryStateIncludingEmptyOnes(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	usage := mgr.Usage()

	for _, state := range dicer.InstanceStates() {
		if _, ok := usage.ByState[state]; !ok {
			t.Errorf("state %s is missing from the usage; an absent series is a gap, not a zero", state)
		}
		if n := usage.ByState[state]; n != 0 {
			t.Errorf("state %s = %d with no instances defined, want 0", state, n)
		}
	}
}

func TestUsageCountsByStateAndSumsHeldResources(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)

	running := dicer.InstanceSpec{ID: "i-run", Name: "run", VCPUs: 2, MemoryBytes: 1 << 30}
	paused := dicer.InstanceSpec{ID: "i-pause", Name: "pause", VCPUs: 1, MemoryBytes: 1 << 29}
	stopped := dicer.InstanceSpec{ID: "i-stop", Name: "stop", VCPUs: 8, MemoryBytes: 1 << 33}

	for _, inst := range []dicer.InstanceSpec{running, paused, stopped} {
		definitions.instances[inst.Name] = inst
	}
	if err := mgr.writeRuntime(dicer.InstanceStatus{
		InstanceID: running.ID, State: dicer.StateRunning, VCPUs: running.VCPUs, MemoryBytes: running.MemoryBytes,
	}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}
	if err := mgr.writeRuntime(dicer.InstanceStatus{
		InstanceID: paused.ID, State: dicer.StatePaused, VCPUs: paused.VCPUs, MemoryBytes: paused.MemoryBytes,
	}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}

	usage := mgr.Usage()

	if got := usage.ByState[dicer.StateRunning]; got != 1 {
		t.Errorf("running = %d, want 1", got)
	}
	if got := usage.ByState[dicer.StatePaused]; got != 1 {
		t.Errorf("paused = %d, want 1", got)
	}
	// An instance with no runtime file has never been started.
	if got := usage.ByState[dicer.StateStopped]; got != 1 {
		t.Errorf("stopped = %d, want 1", got)
	}

	// A paused VM is still resident, so its resources are still committed;
	// a stopped one's are not.
	if want := running.Resources().Add(paused.Resources()); usage.Allocated != want {
		t.Errorf("Allocated = %+v, want %+v", usage.Allocated, want)
	}
	if len(usage.Holders) != 2 {
		t.Errorf("Holders = %+v, want the running and the paused instance", usage.Holders)
	}
}
