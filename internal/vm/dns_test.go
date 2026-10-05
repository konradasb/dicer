// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"testing"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/types"
)

// fakeDNSServers records which networks it was asked to serve and stop
// serving, and fails to serve them if serveErr is set.
type fakeDNSServers struct {
	mu       sync.Mutex
	serving  map[string]bool
	served   []string
	stopped  []string
	serveErr error
}

func (f *fakeDNSServers) Serve(_ context.Context, nw types.Network) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.served = append(f.served, nw.Name)
	if f.serveErr != nil {
		return f.serveErr
	}
	if f.serving == nil {
		f.serving = make(map[string]bool)
	}
	f.serving[nw.Name] = true
	return nil
}

func (f *fakeDNSServers) Stop(network string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.stopped = append(f.stopped, network)
	delete(f.serving, network)
}

// withDNSServers gives a harness a name service, and records the
// nameservers each guest is given.
func withDNSServers(t *testing.T, h *harness) (*fakeDNSServers, func() []string) {
	t.Helper()

	dnsServers := &fakeDNSServers{}
	h.manager.dnsServers = dnsServers

	var (
		mu          sync.Mutex
		nameservers []string
	)
	provision := h.manager.provisionConfigDisk
	h.manager.provisionConfigDisk = func(ctx context.Context, path string, cfg *guest.Config) error {
		mu.Lock()
		nameservers = cfg.Network.DNS.Nameservers
		mu.Unlock()
		return provision(ctx, path, cfg)
	}

	return dnsServers, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return nameservers
	}
}

func TestStartGivesTheGuestTheNetworksDNSServer(t *testing.T) {
	h := newHarness(t)
	dnsServers, nameservers := withDNSServers(t, h)

	h.start(t)

	if got := nameservers(); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Errorf("guest nameservers = %q, want the gateway, where its network's server listens", got)
	}
	if !dnsServers.serving["default"] {
		t.Errorf("served = %q, want the instance's network served", dnsServers.served)
	}

	// The last instance off the network stops its server with its bridge.
	if err := h.manager.Stop(context.Background(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if dnsServers.serving["default"] || !slices.Contains(dnsServers.stopped, "default") {
		t.Errorf("stopped = %q, want the network's server stopped with its bridge", dnsServers.stopped)
	}
}

func TestStartWithoutDNSGivesTheUpstreamNameservers(t *testing.T) {
	h := newHarness(t)
	dnsServers, nameservers := withDNSServers(t, h)
	dnsServers.serveErr = errors.New("address already in use")

	nw := h.definitions.networks["default"]
	nw.Nameservers = []string{"192.0.2.53"}
	h.definitions.networks["default"] = nw

	h.start(t)

	// The instance starts all the same, with the network's own nameservers.
	if got := nameservers(); !slices.Equal(got, []string{"192.0.2.53"}) {
		t.Errorf("guest nameservers = %q, want the network's upstream ones", got)
	}

	// With no name service at all, the same.
	h2 := newHarness(t)
	_, nameservers2 := withDNSServers(t, h2)
	h2.manager.dnsServers = nil
	h2.start(t)
	if got := nameservers2(); !slices.Equal(got, []string{"8.8.8.8"}) {
		t.Errorf("guest nameservers without a name service = %q, want the default upstream", got)
	}
}

func TestLookupFindsRunningInstancesByNameOrHostname(t *testing.T) {
	manager, definitions, _ := newTestManager(t)
	networks, ok := manager.networks.(*fakeNetworks)
	if !ok {
		t.Fatal("test manager's networks are not the fake")
	}

	place := func(name, hostname, network string, state types.InstanceState) netip.Addr {
		t.Helper()
		instance := seedInstance(t, definitions, name)
		instance.Hostname, instance.NetworkName = hostname, network
		definitions.instances[name] = instance
		if _, ok := definitions.networks[network]; !ok {
			definitions.networks[network] = types.Network{Name: network, Subnet: "10.1.0.0/24", Gateway: "10.1.0.1"}
		}
		alloc, err := networks.Allocate(definitions.networks[network], instance.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.writeStatus(types.InstanceStatus{InstanceID: instance.ID, State: state}); err != nil {
			t.Fatal(err)
		}
		return netip.MustParseAddr(alloc.IP)
	}

	db := place("shop-db", "db", "default", types.InstanceStateRunning)
	booting := place("shop-api", "", "default", types.InstanceStateStarting)
	place("shop-old", "", "default", types.InstanceStateStopped)
	place("other-db", "db", "elsewhere", types.InstanceStateRunning)

	tests := []struct {
		name string
		want []netip.Addr
	}{
		{"shop-db", []netip.Addr{db}},
		{"SHOP-DB", []netip.Addr{db}},
		{"db", []netip.Addr{db}}, // by hostname; the other db is on another network
		{"shop-api", []netip.Addr{booting}},
		{"shop-old", nil}, // stopped
		{"other-db", nil}, // another network's
	}
	for _, tt := range tests {
		if got := manager.LookupHost("default", tt.name); !slices.Equal(got, tt.want) {
			t.Errorf("LookupHost(default, %s) = %v, want %v", tt.name, got, tt.want)
		}
	}

	if got := manager.LookupAddr("default", db); !slices.Equal(got, []string{"shop-db", "db"}) {
		t.Errorf("LookupAddr(%s) = %q, want its name and hostname", db, got)
	}
	if got := manager.LookupAddr("default", netip.MustParseAddr("10.0.0.250")); got != nil {
		t.Errorf("LookupAddr of an unheld address = %q, want nothing", got)
	}
}

func TestRecoverServesTheNetworksOfAdoptedInstances(t *testing.T) {
	manager, definitions, _ := newTestManager(t)
	dnsServers := &fakeDNSServers{}
	manager.dnsServers = dnsServers

	instance := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, manager)
	pid := vmm.PID()
	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: instance.ID, State: types.InstanceStateRunning, VMMPID: &pid,
	}); err != nil {
		t.Fatal(err)
	}
	// A stopped instance on another network does not need its server.
	stopped := seedInstance(t, definitions, "idle")
	stopped.NetworkName = "quiet"
	definitions.instances["idle"] = stopped
	definitions.networks["quiet"] = types.Network{Name: "quiet", Subnet: "10.2.0.0/24", Gateway: "10.2.0.1"}

	manager.Recover(context.Background())

	if !slices.Equal(dnsServers.served, []string{"default"}) {
		t.Errorf("served = %q, want the adopted instance's network, and only it", dnsServers.served)
	}
}
