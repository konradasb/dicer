// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

func TestUsageCountsEveryStateIncludingEmptyOnes(t *testing.T) {
	manager, _, _ := newTestManager(t)

	usage := manager.Usage()

	for _, state := range types.InstanceStates() {
		if _, ok := usage.ByState[state]; !ok {
			t.Errorf("state %s is missing from the usage; an absent series is a gap, not a zero", state)
		}
		if n := usage.ByState[state]; n != 0 {
			t.Errorf("state %s = %d with no instances defined, want 0", state, n)
		}
	}
}

func TestUsageCountsByStateAndSumsHeldResources(t *testing.T) {
	manager, definitions, _ := newTestManager(t)

	running := types.InstanceSpec{ID: "i-run", Name: "run", VCPUs: 2, MemoryBytes: 1 << 30}
	paused := types.InstanceSpec{ID: "i-pause", Name: "pause", VCPUs: 1, MemoryBytes: 1 << 29}
	stopped := types.InstanceSpec{ID: "i-stop", Name: "stop", VCPUs: 8, MemoryBytes: 1 << 33}

	for _, instance := range []types.InstanceSpec{running, paused, stopped} {
		definitions.instances[instance.Name] = instance
	}
	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: running.ID, State: types.InstanceStateRunning, VCPUs: running.VCPUs, MemoryBytes: running.MemoryBytes,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: paused.ID, State: types.InstanceStatePaused, VCPUs: paused.VCPUs, MemoryBytes: paused.MemoryBytes,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	usage := manager.Usage()

	if got := usage.ByState[types.InstanceStateRunning]; got != 1 {
		t.Errorf("running = %d, want 1", got)
	}
	if got := usage.ByState[types.InstanceStatePaused]; got != 1 {
		t.Errorf("paused = %d, want 1", got)
	}
	// An instance with no status file has never been started.
	if got := usage.ByState[types.InstanceStateStopped]; got != 1 {
		t.Errorf("stopped = %d, want 1", got)
	}

	// A paused VM is still resident, so its resources are still committed;
	// a stopped one's are not.
	if want := running.Resources().Add(paused.Resources()); usage.Allocated != want {
		t.Errorf("Allocated = %+v, want %+v", usage.Allocated, want)
	}
	if len(usage.Instances) != 2 {
		t.Errorf("Instances = %+v, want the running and the paused instance", usage.Instances)
	}
}
