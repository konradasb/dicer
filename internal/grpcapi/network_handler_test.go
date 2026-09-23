// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"testing"

	"google.golang.org/grpc/codes"

	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

func TestCreateNetworkNormalises(t *testing.T) {
	s, _ := newResourceServer(t)

	n, err := s.CreateNetwork(t.Context(), &dicerdv1.CreateNetworkRequest{Name: "lan", Subnet: "10.9.0.77/24"})
	if err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	if n.GetSubnet() != "10.9.0.0/24" || n.GetGateway() != "10.9.0.1" {
		t.Errorf("subnet %s, gateway %s; want 10.9.0.0/24 and 10.9.0.1", n.GetSubnet(), n.GetGateway())
	}
}

func TestCreateNetworkRefusesWhatCannotWork(t *testing.T) {
	tests := []struct {
		name string
		req  *dicerdv1.CreateNetworkRequest
	}{
		{"IPv6", &dicerdv1.CreateNetworkRequest{Subnet: "fd00::/64"}},
		{"no room for an instance", &dicerdv1.CreateNetworkRequest{Subnet: "10.0.0.0/31"}},
		{"gateway outside", &dicerdv1.CreateNetworkRequest{Subnet: "10.0.0.0/24", Gateway: "10.0.1.1"}},
		{"gateway is the network address", &dicerdv1.CreateNetworkRequest{Subnet: "10.0.0.0/24", Gateway: "10.0.0.0"}},
		{"gateway is the broadcast address", &dicerdv1.CreateNetworkRequest{Subnet: "10.0.0.0/24", Gateway: "10.0.0.255"}},
		{"MTU too small", &dicerdv1.CreateNetworkRequest{Subnet: "10.0.0.0/24", Mtu: 100}},
		{"MTU negative", &dicerdv1.CreateNetworkRequest{Subnet: "10.0.0.0/24", Mtu: -1}},
		{"nameserver not an address", &dicerdv1.CreateNetworkRequest{Subnet: "10.0.0.0/24", Nameservers: []string{"dns"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newResourceServer(t)
			tt.req.Name = "lan"

			_, err := s.CreateNetwork(t.Context(), tt.req)
			wantCode(t, err, codes.InvalidArgument)
		})
	}
}
