// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
)

// resizable makes the harness's instance one with 1 vCPU and 1GiB that can
// grow to 4 vCPUs and 4GiB, on a hypervisor that can resize both, and
// records it running.
func resizable(t *testing.T, h *harness) {
	t.Helper()

	h.instance.VCPUs, h.instance.MemoryBytes = 1, 1<<30
	h.instance.MaxVCPUs, h.instance.MaxMemoryBytes = 4, 4<<30
	if err := h.definitions.UpdateInstance(h.instance); err != nil {
		t.Fatal(err)
	}
	h.hv.capabilities.SupportsHotplugCPU = true
	h.hv.capabilities.SupportsHotplugMemory = true
	h.running(t)
}

func TestResizeChangesARunningInstanceAndItsDefinition(t *testing.T) {
	h := newHarness(t)
	resizable(t, h)

	want := types.Resources{VCPUs: 2, MemoryBytes: 2 << 30}
	if err := h.manager.Resize(t.Context(), h.instance, want); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if h.hv.vCPUs != 2 || h.hv.memoryBytes != 2<<30 {
		t.Errorf("guest resized to %d vCPUs, %d bytes; want 2 vCPUs, 2GiB", h.hv.vCPUs, h.hv.memoryBytes)
	}
	if held := h.status(t).HeldResources(); held != want {
		t.Errorf("holds %s, want %s", held, want)
	}
	// Kept for the next start.
	if saved, _ := h.definitions.Instance(h.instance.Name); saved.Resources() != want {
		t.Errorf("definition asks for %s, want %s", saved.Resources(), want)
	}

	e, ok := h.events.last(events.ActionResized)
	if !ok {
		t.Fatalf("no resized event in %v", h.events.actions())
	}
	if e.Attributes["vcpus"] != "2" || e.Attributes["memory_bytes"] != "2147483648" {
		t.Errorf("attributes = %v", e.Attributes)
	}
}

// TestResizeShrinksToo checks that a running instance can be given less.
func TestResizeShrinksToo(t *testing.T) {
	h := newHarness(t)
	resizable(t, h)
	if err := h.manager.Resize(t.Context(), h.instance, types.Resources{VCPUs: 4, MemoryBytes: 4 << 30}); err != nil {
		t.Fatalf("growing: %v", err)
	}

	want := types.Resources{VCPUs: 1, MemoryBytes: 1 << 30}
	if err := h.manager.Resize(t.Context(), h.instance, want); err != nil {
		t.Fatalf("shrinking: %v", err)
	}
	if held := h.status(t).HeldResources(); held != want {
		t.Errorf("holds %s, want %s", held, want)
	}
}

func TestResizeRefusals(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(h *harness)
		want    types.Resources
		err     error
	}{
		{
			name:    "stopped",
			prepare: func(h *harness) { _ = h.manager.removeRuntimeDir(h.instance.ID) },
			want:    types.Resources{VCPUs: 2, MemoryBytes: 1 << 30},
			err:     errdefs.ErrInvalidState,
		},
		{
			name:    "vCPUs without a maximum",
			prepare: func(h *harness) { h.instance.MaxVCPUs = 0 },
			want:    types.Resources{VCPUs: 2, MemoryBytes: 1 << 30},
			err:     errdefs.ErrInvalidArgument,
		},
		{
			name: "vCPUs beyond the maximum",
			want: types.Resources{VCPUs: 5, MemoryBytes: 1 << 30},
			err:  errdefs.ErrInvalidArgument,
		},
		{
			name:    "memory without a maximum",
			prepare: func(h *harness) { h.instance.MaxMemoryBytes = 0 },
			want:    types.Resources{VCPUs: 1, MemoryBytes: 2 << 30},
			err:     errdefs.ErrInvalidArgument,
		},
		{
			name: "memory beyond the maximum",
			want: types.Resources{VCPUs: 1, MemoryBytes: 5 << 30},
			err:  errdefs.ErrInvalidArgument,
		},
		{
			name:    "vCPUs on a hypervisor that cannot",
			prepare: func(h *harness) { h.hv.capabilities.SupportsHotplugCPU = false },
			want:    types.Resources{VCPUs: 2, MemoryBytes: 1 << 30},
			err:     errors.ErrUnsupported,
		},
		{
			name: "more than the host has room for",
			// testCapacity allows 7GiB, and the maximum is raised past it.
			prepare: func(h *harness) {
				h.manager.capacity = testCapacity
				h.instance.MaxMemoryBytes = 8 << 30
			},
			want: types.Resources{VCPUs: 1, MemoryBytes: 8 << 30},
			err:  errdefs.ErrResourceExhausted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			resizable(t, h)
			if tt.prepare != nil {
				tt.prepare(h)
			}

			err := h.manager.Resize(t.Context(), h.instance, tt.want)
			if !errors.Is(err, tt.err) {
				t.Fatalf("Resize = %v, want %v", err, tt.err)
			}
			if h.hv.vCPUs != 0 || h.hv.memoryBytes != 0 {
				t.Error("the guest was resized anyway")
			}
			if _, ok := h.events.last(events.ActionResized); ok {
				t.Error("a refused resize was recorded")
			}
		})
	}
}

// TestFailedResizeKeepsTheLargerReservation checks that a resize the guest
// fails leaves the instance holding the larger size, since it may yet take
// it, and asking for it from its next start.
func TestFailedResizeKeepsTheLargerReservation(t *testing.T) {
	h := newHarness(t)
	resizable(t, h)
	h.hv.resizeErr = errors.New("the guest did not plug the memory in time")

	want := types.Resources{VCPUs: 1, MemoryBytes: 2 << 30}
	if err := h.manager.Resize(t.Context(), h.instance, want); err == nil {
		t.Fatal("Resize succeeded")
	}

	if held := h.status(t).HeldResources(); held != want {
		t.Errorf("holds %s, want the larger %s", held, want)
	}
	if saved, _ := h.definitions.Instance(h.instance.Name); saved.Resources() != want {
		t.Errorf("definition asks for %s, want %s", saved.Resources(), want)
	}
	if _, ok := h.events.last(events.ActionResized); ok {
		t.Error("a failed resize was recorded")
	}
}

// TestStartLeavesRoomForTheMaximums checks that an instance with maximums
// boots with room to grow to them, and one without boots with none.
func TestStartLeavesRoomForTheMaximums(t *testing.T) {
	tests := []struct {
		name             string
		maxVCPUs         int
		maxMemoryBytes   int64
		wantMaxCount     int
		wantHotplugBytes int64
	}{
		{"none", 0, 0, 0, 0},
		{"both", 4, 3 << 30, 4, 2 << 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.instance.VCPUs, h.instance.MemoryBytes = 1, 1<<30
			h.instance.MaxVCPUs, h.instance.MaxMemoryBytes = tt.maxVCPUs, tt.maxMemoryBytes
			if err := h.definitions.UpdateInstance(h.instance); err != nil {
				t.Fatal(err)
			}

			if err := h.manager.Start(t.Context(), h.instance); err != nil {
				t.Fatalf("Start: %v", err)
			}

			spec := h.starter.spec
			if spec.CPU.MaxCount != tt.wantMaxCount || spec.Memory.HotplugBytes != tt.wantHotplugBytes {
				t.Errorf("started with max %d vCPUs and %d bytes to hotplug; want %d and %d",
					spec.CPU.MaxCount, spec.Memory.HotplugBytes, tt.wantMaxCount, tt.wantHotplugBytes)
			}
		})
	}
}

// TestRefusedResizeChangesNothing checks that a size the hypervisor refuses,
// such as one off its steps, leaves the instance holding and asking for what
// it did.
func TestRefusedResizeChangesNothing(t *testing.T) {
	h := newHarness(t)
	resizable(t, h)
	h.hv.resizeErr = errdefs.InvalidArgument("memory can be resized only in steps of 2 MiB")

	err := h.manager.Resize(t.Context(), h.instance, types.Resources{VCPUs: 1, MemoryBytes: 1<<30 + 1<<20})
	if !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Fatalf("Resize = %v, want the hypervisor's refusal", err)
	}

	before := h.instance.Resources()
	if held := h.status(t).HeldResources(); held != before {
		t.Errorf("holds %s, want %s as before", held, before)
	}
	if saved, _ := h.definitions.Instance(h.instance.Name); saved.Resources() != before {
		t.Errorf("definition asks for %s, want %s as before", saved.Resources(), before)
	}
	if _, ok := h.events.last(events.ActionResized); ok {
		t.Error("a refused resize was recorded")
	}
}
