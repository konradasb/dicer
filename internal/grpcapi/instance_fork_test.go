// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"slices"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// TestForkDefinitionLeavesWhatWasTheSourcesAlone checks that a fork copies
// its source but for what two instances cannot share: an ID, a name, a
// static address and host ports.
func TestForkDefinitionLeavesWhatWasTheSourcesAlone(t *testing.T) {
	source := types.InstanceSpec{
		ID: "source-id", Name: "web", ImageRef: "docker.io/library/nginx:1.27", KernelName: "k",
		VCPUs: 2, MemoryBytes: 512 << 20, DiskBytes: 1 << 30, NetworkName: "default", StaticIP: "10.0.0.5",
		Ports:         []types.PortMapping{{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}},
		Env:           map[string]string{"A": "1"},
		StoppedByUser: true,
	}

	tests := []struct {
		name        string
		req         *dicerdv1.ForkInstanceRequest
		wantNetwork string
		wantIP      string
		wantPorts   []types.PortMapping
	}{
		{
			name:        "nothing given",
			req:         &dicerdv1.ForkInstanceRequest{Name: "web", ForkName: "copy"},
			wantNetwork: "default",
		},
		{
			name: "network, address and ports given",
			req: &dicerdv1.ForkInstanceRequest{
				Name: "web", ForkName: "copy", NetworkName: "lan", StaticIp: "10.1.0.9",
				Ports: []*dicerdv1.PortMapping{{HostPort: 8081, GuestPort: 80}},
			},
			wantNetwork: "lan",
			wantIP:      "10.1.0.9",
			wantPorts:   []types.PortMapping{{HostPort: 8081, GuestPort: 80, Protocol: types.ProtocolTCP}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fork, err := forkDefinition(source, tt.req)
			if err != nil {
				t.Fatalf("forkDefinition: %v", err)
			}

			if fork.ID == "" || fork.ID == source.ID || fork.Name != "copy" {
				t.Errorf("fork is %s (%s), want copy with an ID of its own", fork.Name, fork.ID)
			}
			if fork.NetworkName != tt.wantNetwork || fork.StaticIP != tt.wantIP {
				t.Errorf("fork's network = %s %q, want %s %q", fork.NetworkName, fork.StaticIP, tt.wantNetwork, tt.wantIP)
			}
			if !slices.Equal(fork.Ports, tt.wantPorts) {
				t.Errorf("fork's ports = %v, want %v", fork.Ports, tt.wantPorts)
			}
			if fork.ImageRef != source.ImageRef || fork.VCPUs != source.VCPUs || fork.Env["A"] != "1" {
				t.Errorf("fork = %+v, want the rest of the source's definition", fork)
			}
			if fork.StoppedByUser {
				t.Error("the fork is recorded as stopped by a user, as its source was")
			}
		})
	}

	if _, err := forkDefinition(source, &dicerdv1.ForkInstanceRequest{Name: "web", ForkName: "../bad"}); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("forkDefinition with an invalid name = %v, want ErrInvalidArgument", err)
	}
}
