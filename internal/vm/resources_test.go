// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// testCapacity is a host with 4 CPUs and 8GiB, overcommitted as the daemon
// does by default: 16 vCPUs, and 7GiB once 1GiB is reserved.
var testCapacity = types.Capacity{
	Host:                types.Resources{VCPUs: 4, MemoryBytes: 8 << 30},
	ReservedMemoryBytes: 1 << 30,
	CPUOvercommit:       4,
	MemoryOvercommit:    1,
}

func TestCapacityAllocatable(t *testing.T) {
	want := types.Resources{VCPUs: 16, MemoryBytes: 7 << 30}
	if got := testCapacity.Allocatable(); got != want {
		t.Errorf("Allocatable = %+v, want %+v", got, want)
	}
}

// admitHarness is a harness on a host with testCapacity, and a second
// instance to compete with.
func admitHarness(t *testing.T) (*harness, types.InstanceSpec) {
	t.Helper()

	h := newHarness(t)
	h.mgr.capacity = testCapacity

	definitions, ok := h.mgr.definitions.(*fakeDefinitions)
	if !ok {
		t.Fatal("harness definitions are not fake")
	}
	other := seedInstance(t, definitions, "other")

	return h, other
}

// holding records inst as in state, holding r, as admission would have.
func holding(t *testing.T, mgr *Manager, inst types.InstanceSpec, state types.InstanceState, r types.Resources) {
	t.Helper()

	if err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID: inst.ID, State: state, VCPUs: r.VCPUs, MemoryBytes: r.MemoryBytes,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStartRefusedWhenTheHostIsFull(t *testing.T) {
	h, other := admitHarness(t)
	h.inst.VCPUs, h.inst.MemoryBytes = 1, 2<<30

	// 6 of the 7GiB are held: 2 more do not fit.
	holding(t, h.mgr, other, types.StateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	err := h.mgr.Start(t.Context(), h.inst)
	if !errors.Is(err, errdefs.ErrResourceExhausted) {
		t.Fatalf("Start = %v, want ErrResourceExhausted", err)
	}

	// A refusal changes nothing: the instance was never started, so it is
	// not Failed either.
	if rt := h.runtime(t); rt.State != types.StateStopped {
		t.Errorf("state after a refused start = %s, want Stopped", rt.State)
	}
	if len(h.starter.vmms) != 0 {
		t.Error("a VMM was started for a refused instance")
	}
}

func TestStartAdmittedWhenItFits(t *testing.T) {
	h, other := admitHarness(t)
	h.inst.VCPUs, h.inst.MemoryBytes = 1, 1<<30

	holding(t, h.mgr, other, types.StateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	if err := h.mgr.Start(t.Context(), h.inst); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// What it holds is recorded, and is what it asked for.
	rt := h.runtime(t)
	if got := rt.Held(); got != h.inst.Resources() {
		t.Errorf("held = %+v, want %+v", got, h.inst.Resources())
	}
}

// Only instances with a VMM, or about to have one, hold anything.
func TestWhatHoldsResources(t *testing.T) {
	for _, tc := range []struct {
		state types.InstanceState
		holds bool
	}{
		{types.StateStarting, true},
		{types.StateRunning, true},
		{types.StatePaused, true},
		{types.StateStopping, false},
		{types.StateStopped, false},
		{types.StateFailed, false},
	} {
		h, other := admitHarness(t)
		h.inst.VCPUs, h.inst.MemoryBytes = 1, 2<<30
		holding(t, h.mgr, other, tc.state, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

		err := h.mgr.Start(t.Context(), h.inst)
		if refused := errors.Is(err, errdefs.ErrResourceExhausted); refused != tc.holds {
			t.Errorf("another instance %s: Start = %v, want refused %v", tc.state, err, tc.holds)
		}
	}
}

// A restored guest comes back with the memory it was snapshotted with, and
// is admitted on that, not on what the definition says now.
func TestRestoreAdmittedOnTheSnapshotsMemory(t *testing.T) {
	h, other := admitHarness(t)
	h.inst.MemoryBytes = 1 << 30
	h.start(t)

	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "big"); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatal(err)
	}

	// Rewrite the snapshot as if taken with 4GiB, and fill the host so
	// only 1GiB is left: enough for the definition, not for the snapshot.
	snapshotWithMemory(t, h, "big", 4<<30)
	holding(t, h.mgr, other, types.StateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "big")
	if !errors.Is(err, errdefs.ErrResourceExhausted) {
		t.Errorf("RestoreSnapshot = %v, want ErrResourceExhausted", err)
	}
}

// snapshotWithMemory rewrites a snapshot's metadata as if it had been taken
// with memoryBytes of guest memory.
func snapshotWithMemory(t *testing.T, h *harness, name string, memoryBytes int64) {
	t.Helper()

	path := h.mgr.snapshotMetadataPath(h.inst, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var snap types.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	snap.MemoryBytes = memoryBytes

	if data, err = json.Marshal(snap); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Two starts competing for the last room on the host: exactly one is let in.
func TestConcurrentStartsCannotBothTakeTheLastRoom(t *testing.T) {
	h, other := admitHarness(t)
	h.inst.VCPUs, h.inst.MemoryBytes = 1, 1<<30
	other.VCPUs, other.MemoryBytes = 1, 1<<30

	definitions, _ := h.mgr.definitions.(*fakeDefinitions)
	third := seedInstance(t, definitions, "third")
	holding(t, h.mgr, third, types.StateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	// The two starts never boot: admission is all that is under test, so
	// each is refused or admitted and then left Starting.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, inst := range []types.InstanceSpec{h.inst, other} {
		wg.Go(func() {
			lock := h.mgr.lock(inst.ID)
			lock.Lock()
			defer lock.Unlock()
			results[i] = h.mgr.admit(inst, inst.Resources())
		})
	}
	wg.Wait()

	admitted := 0
	for _, err := range results {
		switch {
		case err == nil:
			admitted++
		case !errors.Is(err, errdefs.ErrResourceExhausted):
			t.Errorf("admit = %v", err)
		}
	}
	if admitted != 1 {
		t.Errorf("%d starts admitted into room for one", admitted)
	}
}

func TestCheckResources(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	mgr.capacity = testCapacity

	if err := mgr.CheckResources(types.Resources{VCPUs: 4, MemoryBytes: 7 << 30}); err != nil {
		t.Errorf("the whole host: %v", err)
	}
	// More vCPUs than CPUs is never useful to one instance, overcommit or
	// not.
	if err := mgr.CheckResources(types.Resources{VCPUs: 5, MemoryBytes: 1 << 30}); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("5 vCPUs on 4 CPUs: %v, want ErrInvalidArgument", err)
	}
	if err := mgr.CheckResources(types.Resources{VCPUs: 1, MemoryBytes: 8 << 30}); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("more memory than allocatable: %v, want ErrInvalidArgument", err)
	}

	// Without a capacity, nothing is refused.
	mgr.capacity = types.Capacity{}
	if err := mgr.CheckResources(types.Resources{VCPUs: 1000, MemoryBytes: 1 << 50}); err != nil {
		t.Errorf("unlimited: %v", err)
	}
}

// The refusal says what was asked for and what is left, in the units sizes
// are given in.
func TestRefusalExplainsItself(t *testing.T) {
	h, other := admitHarness(t)
	h.inst.VCPUs, h.inst.MemoryBytes = 1, 2<<30
	holding(t, h.mgr, other, types.StateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	err := h.mgr.Start(t.Context(), h.inst)

	want := `instance "web" needs 1 vCPU, 2 GiB, but 1 vCPU, 6 GiB of the 16 vCPU, 7 GiB ` +
		`this host allows is committed`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}
