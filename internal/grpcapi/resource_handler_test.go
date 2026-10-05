// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"fmt"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestGetResources(t *testing.T) {
	s, definitions := newTestServer(t)

	for _, instance := range []types.InstanceSpec{
		{ID: "i-1", Name: "web", VCPUs: 2, MemoryBytes: 1 << 30, DiskBytes: 10 << 30},
		{ID: "i-2", Name: "db", VCPUs: 1, MemoryBytes: 1 << 30, DiskBytes: 20 << 30},
	} {
		if err := definitions.CreateInstance(instance); err != nil {
			t.Fatal(err)
		}
	}
	if err := definitions.CreateVolume(types.Volume{ID: "v-1", Name: "data", SizeBytes: 5 << 30}); err != nil {
		t.Fatal(err)
	}

	resp, err := s.GetResources(t.Context(), &dicerdv1.GetResourcesRequest{})
	if err != nil {
		t.Fatalf("GetResources: %v", err)
	}

	// Nothing is running, so nothing is held, and everything is left.
	cpu := resp.GetCpu()
	if cpu.GetHost() != 4 || cpu.GetOvercommit() != 4 || cpu.GetAllocatable() != 16 ||
		cpu.GetAllocated() != 0 || cpu.GetAvailable() != 16 {
		t.Errorf("cpu = %v, want 4 CPUs overcommitted to 16, all available", cpu)
	}
	memory := resp.GetMemory()
	if memory.GetReserved() != 1<<30 || memory.GetAllocatable() != 7<<30 || memory.GetAvailable() != 7<<30 {
		t.Errorf("memory = %v, want 7GiB allocatable after a 1GiB reserve", memory)
	}
	if len(resp.GetInstances()) != 0 {
		t.Errorf("instances = %v, want none holding resources", resp.GetInstances())
	}

	// Provisioned disk is promised, not used: every overlay and volume.
	if got, want := resp.GetDisk().GetProvisionedBytes(), int64(35<<30); got != want {
		t.Errorf("provisioned = %d, want %d", got, want)
	}
	if resp.GetDisk().GetTotalBytes() <= 0 {
		t.Errorf("disk = %v, want the filesystem's size", resp.GetDisk())
	}
}

// A start refused for want of room is told apart from a failure: the caller
// can wait for something to stop, or make room.
func TestResourceExhaustedStatus(t *testing.T) {
	err := fmt.Errorf("instance %q needs more: %w", "web", errdefs.ErrResourceExhausted)
	wantClass(t, err, errdefs.ErrResourceExhausted)

	// The subnet running out is the same kind of refusal.
	wantClass(t, network.ErrNoAvailableIPs, errdefs.ErrResourceExhausted)
	if !errors.Is(network.ErrNoAvailableIPs, errdefs.ErrResourceExhausted) {
		t.Error("ErrNoAvailableIPs does not wrap ErrResourceExhausted")
	}
}
