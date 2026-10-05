// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// A definition that could never start is refused when it is written, not at
// its first start.
func TestCreateInstanceTooBigForTheHost(t *testing.T) {
	s, definitions := newTestServer(t)
	seedKernelAndNetwork(t, definitions)

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
		wantClass(t, err, errdefs.ErrInvalidArgument)
		if _, err := definitions.Instance("big"); err == nil {
			t.Errorf("%s: the definition was recorded", tc.name)
		}
	}
}

// seedKernelAndNetwork defines the kernel k and the network default, which
// an instance needs to be created.
func seedKernelAndNetwork(t *testing.T, definitions *filestore.Manager) {
	t.Helper()

	if err := definitions.CreateKernel(types.Kernel{ID: "k-1", Name: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := definitions.CreateNetwork(types.Network{
		ID: "n-1", Name: "default", Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Bridge: "dicer-default",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateMaximums(t *testing.T) {
	tests := []struct {
		name           string
		hypervisor     types.HypervisorType
		maxVCPUs       int
		maxMemoryBytes int64
		valid          bool
	}{
		{name: "none", valid: true},
		{name: "room to grow", maxVCPUs: 4, maxMemoryBytes: 4 << 30, valid: true},
		{name: "as much as it asks for", maxVCPUs: 2, maxMemoryBytes: 2 << 30, valid: true},
		{name: "fewer vCPUs than it asks for", maxVCPUs: 1},
		{name: "less memory than it asks for", maxMemoryBytes: 1 << 30},
		{name: "negative", maxVCPUs: -1},
		{name: "vCPUs on firecracker", hypervisor: types.HypervisorTypeFirecracker, maxVCPUs: 4},
		{name: "memory on firecracker", hypervisor: types.HypervisorTypeFirecracker, maxMemoryBytes: 4 << 30, valid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMaximums(types.InstanceSpec{
				HypervisorType: tt.hypervisor, VCPUs: 2, MemoryBytes: 2 << 30,
				MaxVCPUs: tt.maxVCPUs, MaxMemoryBytes: tt.maxMemoryBytes,
			})
			if tt.valid {
				if err != nil {
					t.Errorf("validateMaximums = %v, want nil", err)
				}
				return
			}
			wantClass(t, err, errdefs.ErrInvalidArgument)
		})
	}
}

// An instance whose maximum the host could never give it is refused when it
// is created.
func TestCreateInstanceWithAMaximumTooBigForTheHost(t *testing.T) {
	s, definitions := newTestServer(t)
	seedKernelAndNetwork(t, definitions)

	_, err := s.CreateInstance(t.Context(), &dicerdv1.CreateInstanceRequest{
		Name: "web", ImageRef: "alpine", KernelName: "k", NetworkName: "default",
		Vcpus: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 30, MaxMemoryBytes: 8 << 30,
	})
	wantClass(t, err, errdefs.ErrInvalidArgument)
}

func TestResizeInstanceRefusals(t *testing.T) {
	s, definitions := newTestServer(t)
	seedKernelAndNetwork(t, definitions)
	if err := definitions.CreateInstance(types.InstanceSpec{
		ID: "i-1", Name: "web", ImageRef: "alpine", KernelName: "k", NetworkName: "default",
		VCPUs: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 30, MaxVCPUs: 4, MaxMemoryBytes: 4 << 30,
	}); err != nil {
		t.Fatal(err)
	}

	two, none := int32(2), int32(0)
	tests := []struct {
		name string
		req  *dicerdv1.ResizeInstanceRequest
		err  error
	}{
		{"nothing to change", &dicerdv1.ResizeInstanceRequest{Name: "web"}, errdefs.ErrInvalidArgument},
		{"no vCPUs", &dicerdv1.ResizeInstanceRequest{Name: "web", Vcpus: &none}, errdefs.ErrInvalidArgument},
		{"not running", &dicerdv1.ResizeInstanceRequest{Name: "web", Vcpus: &two}, errdefs.ErrInvalidState},
		{"no such instance", &dicerdv1.ResizeInstanceRequest{Name: "db", Vcpus: &two}, errdefs.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.ResizeInstance(t.Context(), tt.req)
			wantClass(t, err, tt.err)
		})
	}
}
