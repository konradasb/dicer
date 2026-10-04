// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
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
			wantClass(t, err, errdefs.ErrInvalidArgument)
		})
	}
}

// A subnet another network or the host is on is refused as taken, which
// dicer compose up tells apart from a request it got wrong.
func TestCreateNetworkRefusesATakenSubnet(t *testing.T) {
	s, _ := newResourceServer(t)
	s.hostSubnets = func() ([]netip.Prefix, error) {
		return []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}, nil
	}
	if _, err := s.CreateNetwork(t.Context(), &dicerdv1.CreateNetworkRequest{Name: "lan", Subnet: "10.9.0.0/24"}); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	for name, subnet := range map[string]string{"another network's": "10.9.0.0/16", "the host's": "192.168.0.0/16"} {
		t.Run(name, func(t *testing.T) {
			_, err := s.CreateNetwork(t.Context(), &dicerdv1.CreateNetworkRequest{Name: "other", Subnet: subnet})
			wantClass(t, err, errdefs.ErrExists)
		})
	}
}

// fakeRecorder keeps the events recorded.
type fakeRecorder struct {
	events []events.Event
}

func (f *fakeRecorder) Record(e events.Event) { f.events = append(f.events, e) }

// TestNetworkCreatedAndDeletedAreRecorded checks a network's creation and
// deletion are recorded, with its subnet and gateway, and a refused request
// is not.
func TestNetworkCreatedAndDeletedAreRecorded(t *testing.T) {
	s, _ := newResourceServer(t)
	recorded := &fakeRecorder{}
	s.networkHandler.events = recorded

	n, err := s.CreateNetwork(t.Context(), &dicerdv1.CreateNetworkRequest{Name: "lan", Subnet: "10.9.0.0/24"})
	if err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	if _, err := s.CreateNetwork(t.Context(), &dicerdv1.CreateNetworkRequest{Name: "lan", Subnet: "10.8.0.0/24"}); err == nil {
		t.Fatal("CreateNetwork of a name taken succeeded")
	}
	if _, err := s.DeleteNetwork(t.Context(), &dicerdv1.DeleteNetworkRequest{Name: "lan"}); err != nil {
		t.Fatalf("DeleteNetwork: %v", err)
	}

	want := []events.Action{events.ActionCreated, events.ActionDeleted}
	if len(recorded.events) != len(want) {
		t.Fatalf("recorded %+v, want %v", recorded.events, want)
	}
	for i, e := range recorded.events {
		if e.Kind != events.KindNetwork || e.ID != n.GetId() || e.Name != "lan" || e.Action != want[i] {
			t.Errorf("event %d = %+v, want network lan %s", i, e, want[i])
		}
		if e.Attributes["subnet"] != "10.9.0.0/24" || e.Attributes["gateway"] != "10.9.0.1" {
			t.Errorf("event %d attributes = %v, want its subnet and gateway", i, e.Attributes)
		}
		if !strings.Contains(e.Message, "10.9.0.0/24") {
			t.Errorf("event %d message = %q, want it to name the subnet", i, e.Message)
		}
	}
}
