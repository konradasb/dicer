// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// A definition that could never start is refused when it is written, not at
// its first start.
func TestCreateInstanceTooBigForTheHost(t *testing.T) {
	s, definitions := newTestServer(t)

	if err := definitions.CreateKernel(types.Kernel{ID: "k-1", Name: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := definitions.CreateNetwork(types.Network{
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
		wantClass(t, err, errdefs.ErrInvalidArgument)
		if _, err := definitions.Instance("big"); err == nil {
			t.Errorf("%s: the definition was recorded", tc.name)
		}
	}
}
