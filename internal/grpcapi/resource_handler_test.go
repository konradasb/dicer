// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/network"
	"github.com/dicer-sh/dicer/internal/vm"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// testCapacity is a 4-CPU, 8GiB host with the daemon's default admission:
// 16 vCPUs and 7GiB.
var testCapacity = dicer.Capacity{
	Host:                dicer.Resources{VCPUs: 4, MemoryBytes: 8 << 30},
	ReservedMemoryBytes: 1 << 30,
	CPUOvercommit:       4,
	MemoryOvercommit:    1,
}

// newResourceServer returns a Server over a real definition store and a
// lifecycle manager with testCapacity, and the store.
func newResourceServer(t *testing.T) (*Server, *filestore.Manager) {
	t.Helper()

	logger := slog.New(slog.DiscardHandler)
	dataDir := filepath.Join(t.TempDir(), "data")

	definitions, err := filestore.NewManager(filestore.Config{DataDir: dataDir, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}

	instances := vm.NewManager(vm.Config{
		Definitions: definitions,
		RunDir:      filepath.Join(t.TempDir(), "run"),
		Capacity:    testCapacity,
		Logger:      logger,
	})

	return NewServer(Config{
		Definitions: definitions,
		Instances:   instances,
		DataDir:     dataDir,
		Logger:      logger,
	}), definitions
}

func TestGetResources(t *testing.T) {
	s, definitions := newResourceServer(t)

	for _, inst := range []dicer.InstanceSpec{
		{ID: "i-1", Name: "web", VCPUs: 2, MemoryBytes: 1 << 30, DiskBytes: 10 << 30},
		{ID: "i-2", Name: "db", VCPUs: 1, MemoryBytes: 1 << 30, DiskBytes: 20 << 30},
	} {
		if err := definitions.CreateInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	if err := definitions.CreateVolume(dicer.Volume{ID: "v-1", Name: "data", SizeBytes: 5 << 30}); err != nil {
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

// A definition that could never start is refused when it is written, not at
// its first start.
func TestCreateInstanceTooBigForTheHost(t *testing.T) {
	s, definitions := newResourceServer(t)

	if err := definitions.CreateKernel(dicer.Kernel{ID: "k-1", Name: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := definitions.CreateNetwork(dicer.Network{
		ID: "n-1", Name: "default", Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Bridge: "dicer-default",
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		vcpus  int32
		memory int64
	}{
		{"more vCPUs than CPUs", 5, 1 << 30},
		{"more memory than allocatable", 1, 8 << 30},
	} {
		_, err := s.CreateInstance(t.Context(), &dicerdv1.CreateInstanceRequest{
			Name: "big", ImageRef: "alpine", KernelName: "k", NetworkName: "default",
			Vcpus: tc.vcpus, MemoryBytes: tc.memory, DiskBytes: 1 << 30,
		})
		wantCode(t, err, codes.InvalidArgument)
		if _, err := definitions.GetInstance("big"); err == nil {
			t.Errorf("%s: the definition was recorded", tc.name)
		}
	}
}

// A start refused for want of room is told apart from a failure: the caller
// can wait for something to stop, or make room.
func TestResourceExhaustedStatus(t *testing.T) {
	err := fmt.Errorf("instance %q needs more: %w", "web", dicer.ErrResourceExhausted)
	wantCode(t, toStatus(err), codes.ResourceExhausted)

	// The subnet running out is the same kind of refusal.
	wantCode(t, toStatus(network.ErrNoAvailableIPs), codes.ResourceExhausted)
	if !errors.Is(network.ErrNoAvailableIPs, dicer.ErrResourceExhausted) {
		t.Error("ErrNoAvailableIPs does not wrap ErrResourceExhausted")
	}
}
