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

// admitHarness is a harness on a host with testCapacity, and a second
// instance to compete with.
func admitHarness(t *testing.T) (*harness, types.InstanceSpec) {
	t.Helper()

	h := newHarness(t)
	h.manager.capacity = testCapacity

	definitions, ok := h.manager.definitions.(*fakeDefinitions)
	if !ok {
		t.Fatal("harness definitions are not fake")
	}
	other := seedInstance(t, definitions, "other")

	return h, other
}

// holding records instance as in state, holding r, as admission would have.
func holding(t *testing.T, manager *Manager, instance types.InstanceSpec, state types.InstanceState, r types.Resources) {
	t.Helper()

	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: instance.ID, State: state, VCPUs: r.VCPUs, MemoryBytes: r.MemoryBytes,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStartRefusedWhenTheHostIsFull(t *testing.T) {
	h, other := admitHarness(t)
	h.instance.VCPUs, h.instance.MemoryBytes = 1, 2<<30

	// 6 of the 7GiB are held: 2 more do not fit.
	holding(t, h.manager, other, types.InstanceStateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	err := h.manager.Start(t.Context(), h.instance)
	if !errors.Is(err, errdefs.ErrResourceExhausted) {
		t.Fatalf("Start = %v, want ErrResourceExhausted", err)
	}

	// A refusal changes nothing: the instance was never started, so it is
	// not Failed either.
	if status := h.status(t); status.State != types.InstanceStateStopped {
		t.Errorf("state after a refused start = %s, want Stopped", status.State)
	}
	if h.starter.vmmCount() != 0 {
		t.Error("a VMM was started for a refused instance")
	}
}

func TestStartAdmittedWhenItFits(t *testing.T) {
	h, other := admitHarness(t)
	h.instance.VCPUs, h.instance.MemoryBytes = 1, 1<<30

	holding(t, h.manager, other, types.InstanceStateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	if err := h.manager.Start(t.Context(), h.instance); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// What it holds is recorded, and is what it asked for.
	status := h.status(t)
	if got := status.HeldResources(); got != h.instance.Resources() {
		t.Errorf("held = %+v, want %+v", got, h.instance.Resources())
	}
}

// Only instances with a VMM, or about to have one, hold anything.
func TestWhatHoldsResources(t *testing.T) {
	for _, tt := range []struct {
		state types.InstanceState
		holds bool
	}{
		{types.InstanceStateStarting, true},
		{types.InstanceStateRunning, true},
		{types.InstanceStatePaused, true},
		{types.InstanceStateStopping, false},
		{types.InstanceStateStopped, false},
		{types.InstanceStateFailed, false},
	} {
		t.Run(string(tt.state), func(t *testing.T) {
			h, other := admitHarness(t)
			h.instance.VCPUs, h.instance.MemoryBytes = 1, 2<<30
			holding(t, h.manager, other, tt.state, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

			err := h.manager.Start(t.Context(), h.instance)
			if refused := errors.Is(err, errdefs.ErrResourceExhausted); refused != tt.holds {
				t.Errorf("another instance %s: Start = %v, want refused %v", tt.state, err, tt.holds)
			}
		})
	}
}

// A restored guest comes back with the memory it was snapshotted with, and
// is admitted on that, not on what the definition says now.
func TestRestoreAdmittedOnTheSnapshotsMemory(t *testing.T) {
	h, other := admitHarness(t)
	h.instance.MemoryBytes = 1 << 30
	h.start(t)

	if _, err := h.manager.CreateSnapshot(t.Context(), h.instance, "big"); err != nil {
		t.Fatal(err)
	}
	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatal(err)
	}

	// Rewrite the snapshot as if taken with 4GiB, and fill the host so
	// only 1GiB is left: enough for the definition, not for the snapshot.
	snapshotWithMemory(t, h, "big", 4<<30)
	holding(t, h.manager, other, types.InstanceStateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	err := h.manager.RestoreSnapshot(t.Context(), h.instance, "big")
	if !errors.Is(err, errdefs.ErrResourceExhausted) {
		t.Errorf("RestoreSnapshot = %v, want ErrResourceExhausted", err)
	}
}

// snapshotWithMemory rewrites a snapshot's metadata as if it had been taken
// with memoryBytes of guest memory.
func snapshotWithMemory(t *testing.T, h *harness, name string, memoryBytes int64) {
	t.Helper()

	path := h.manager.snapshotMetadataPath(h.instance, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var snapshot types.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.MemoryBytes = memoryBytes

	if data, err = json.Marshal(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Two starts competing for the last room on the host: exactly one is let in.
func TestConcurrentStartsCannotBothTakeTheLastRoom(t *testing.T) {
	h, other := admitHarness(t)
	h.instance.VCPUs, h.instance.MemoryBytes = 1, 1<<30
	other.VCPUs, other.MemoryBytes = 1, 1<<30

	definitions, _ := h.manager.definitions.(*fakeDefinitions)
	third := seedInstance(t, definitions, "third")
	holding(t, h.manager, third, types.InstanceStateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	// The two starts never boot: admission is all that is under test, so
	// each is refused or admitted and then left Starting.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, instance := range []types.InstanceSpec{h.instance, other} {
		wg.Go(func() {
			lock := h.manager.lock(instance.ID)
			lock.Lock()
			defer lock.Unlock()
			results[i] = h.manager.admit(instance, instance.Resources())
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
	manager, _, _ := newTestManager(t)
	manager.capacity = testCapacity

	if err := manager.CheckResources(types.Resources{VCPUs: 4, MemoryBytes: 7 << 30}); err != nil {
		t.Errorf("the whole host: %v", err)
	}
	// More vCPUs than CPUs is never useful to one instance, overcommit or
	// not.
	if err := manager.CheckResources(types.Resources{VCPUs: 5, MemoryBytes: 1 << 30}); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("5 vCPUs on 4 CPUs: %v, want ErrInvalidArgument", err)
	}
	if err := manager.CheckResources(types.Resources{VCPUs: 1, MemoryBytes: 8 << 30}); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("more memory than allocatable: %v, want ErrInvalidArgument", err)
	}

	// Without a capacity, nothing is refused.
	manager.capacity = types.Capacity{}
	if err := manager.CheckResources(types.Resources{VCPUs: 1000, MemoryBytes: 1 << 50}); err != nil {
		t.Errorf("unlimited: %v", err)
	}
}

// The refusal says what was asked for and what is left, in the units sizes
// are given in.
func TestRefusalExplainsItself(t *testing.T) {
	h, other := admitHarness(t)
	h.instance.VCPUs, h.instance.MemoryBytes = 1, 2<<30
	holding(t, h.manager, other, types.InstanceStateRunning, types.Resources{VCPUs: 1, MemoryBytes: 6 << 30})

	err := h.manager.Start(t.Context(), h.instance)

	want := `instance "web" needs 1 vCPU, 2 GiB, but 1 vCPU, 6 GiB of the 16 vCPU, 7 GiB ` +
		`this host allows is committed`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// dataMount mounts the volume data at /data.
func dataMount(readOnly bool) []types.Mount {
	return []types.Mount{{Type: types.MountTypeVolume, Source: "data", Target: "/data", ReadOnly: readOnly}}
}

// seedVolumeHolder defines another instance mounting data, recorded as in
// state.
func (h *harness) seedVolumeHolder(t *testing.T, state types.InstanceState, readOnly bool) {
	t.Helper()

	other := seedInstance(t, h.definitions, "other")
	other.Mounts = dataMount(readOnly)
	h.definitions.instances[other.Name] = other

	if err := h.manager.writeStatus(types.InstanceStatus{InstanceID: other.ID, State: state}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
}

func TestAdmitVolumeSharing(t *testing.T) {
	const rw, ro = false, true
	tests := []struct {
		name    string
		mine    bool // read-only
		theirs  bool
		state   types.InstanceState
		refused bool
	}{
		{"read-write beside running read-write", rw, rw, types.InstanceStateRunning, true},
		{"read-write beside starting read-write", rw, rw, types.InstanceStateStarting, true},
		{"read-write beside stopping read-write", rw, rw, types.InstanceStateStopping, true},
		{"read-write beside running read-only", rw, ro, types.InstanceStateRunning, true},
		{"read-only beside paused read-write", ro, rw, types.InstanceStatePaused, true},
		{"read-only beside running read-only", ro, ro, types.InstanceStateRunning, false},
		{"read-write beside stopped read-write", rw, rw, types.InstanceStateStopped, false},
		{"read-write beside failed read-write", rw, rw, types.InstanceStateFailed, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.instance.Mounts = dataMount(tt.mine)
			h.definitions.instances[h.instance.Name] = h.instance
			h.seedVolumeHolder(t, tt.state, tt.theirs)

			err := h.manager.admit(h.instance, h.instance.Resources())
			if refused := errors.Is(err, errdefs.ErrInvalidState); refused != tt.refused {
				t.Fatalf("admit = %v, want refused %v", err, tt.refused)
			}
			if tt.refused {
				if status := h.status(t); status.State != types.InstanceStateStopped {
					t.Errorf("state = %s, want a refused instance left %s", status.State, types.InstanceStateStopped)
				}
			}
		})
	}
}
