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

	ns := &fakeDNSServers{}
	h.mgr.dnsServers = ns

	var (
		mu          sync.Mutex
		nameservers []string
	)
	provision := h.mgr.provisionConfigDisk
	h.mgr.provisionConfigDisk = func(ctx context.Context, path string, cfg *guest.Config) error {
		mu.Lock()
		nameservers = cfg.Network.DNS.Nameservers
		mu.Unlock()
		return provision(ctx, path, cfg)
	}

	return ns, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return nameservers
	}
}

func TestStartGivesTheGuestTheNetworksDNSServer(t *testing.T) {
	h := newHarness(t)
	ns, nameservers := withDNSServers(t, h)

	h.start(t)

	if got := nameservers(); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Errorf("guest nameservers = %q, want the gateway, where its network's server listens", got)
	}
	if !ns.serving["default"] {
		t.Errorf("served = %q, want the instance's network served", ns.served)
	}

	// The last instance off the network stops its server with its bridge.
	if err := h.mgr.Stop(context.Background(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ns.serving["default"] || !slices.Contains(ns.stopped, "default") {
		t.Errorf("stopped = %q, want the network's server stopped with its bridge", ns.stopped)
	}
}

func TestStartWithoutDNSGivesTheUpstreamNameservers(t *testing.T) {
	h := newHarness(t)
	ns, nameservers := withDNSServers(t, h)
	ns.serveErr = errors.New("address already in use")

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
	h2.mgr.dnsServers = nil
	h2.start(t)
	if got := nameservers2(); !slices.Equal(got, []string{"8.8.8.8"}) {
		t.Errorf("guest nameservers without a name service = %q, want the default upstream", got)
	}
}

func TestLookupFindsRunningInstancesByNameOrHostname(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	networks, ok := mgr.networks.(*fakeNetworks)
	if !ok {
		t.Fatal("test manager's networks are not the fake")
	}

	place := func(name, hostname, network string, state types.InstanceState) netip.Addr {
		t.Helper()
		inst := seedInstance(t, definitions, name)
		inst.Hostname, inst.NetworkName = hostname, network
		definitions.instances[name] = inst
		if _, ok := definitions.networks[network]; !ok {
			definitions.networks[network] = types.Network{Name: network, Subnet: "10.1.0.0/24", Gateway: "10.1.0.1"}
		}
		alloc, err := networks.Allocate(definitions.networks[network], inst.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := mgr.writeRuntime(types.InstanceStatus{InstanceID: inst.ID, State: state}); err != nil {
			t.Fatal(err)
		}
		return netip.MustParseAddr(alloc.IP)
	}

	db := place("shop-db", "db", "default", types.StateRunning)
	booting := place("shop-api", "", "default", types.StateStarting)
	place("shop-old", "", "default", types.StateStopped)
	place("other-db", "db", "elsewhere", types.StateRunning)

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
		if got := mgr.LookupHost("default", tt.name); !slices.Equal(got, tt.want) {
			t.Errorf("LookupHost(default, %s) = %v, want %v", tt.name, got, tt.want)
		}
	}

	if got := mgr.LookupAddr("default", db); !slices.Equal(got, []string{"shop-db", "db"}) {
		t.Errorf("LookupAddr(%s) = %q, want its name and hostname", db, got)
	}
	if got := mgr.LookupAddr("default", netip.MustParseAddr("10.0.0.250")); got != nil {
		t.Errorf("LookupAddr of an unheld address = %q, want nothing", got)
	}
}

func TestRecoverServesTheNetworksOfAdoptedInstances(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	ns := &fakeDNSServers{}
	mgr.dnsServers = ns

	inst := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, mgr)
	pid := vmm.PID()
	if err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID: inst.ID, State: types.StateRunning, HypervisorPID: &pid,
	}); err != nil {
		t.Fatal(err)
	}
	// A stopped instance on another network does not need its server.
	stopped := seedInstance(t, definitions, "idle")
	stopped.NetworkName = "quiet"
	definitions.instances["idle"] = stopped
	definitions.networks["quiet"] = types.Network{Name: "quiet", Subnet: "10.2.0.0/24", Gateway: "10.2.0.1"}

	if err := mgr.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if !slices.Equal(ns.served, []string{"default"}) {
		t.Errorf("served = %q, want the adopted instance's network, and only it", ns.served)
	}
}
