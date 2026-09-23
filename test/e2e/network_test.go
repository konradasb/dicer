// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"slices"
	"testing"
)

// TestNetworkGuestReachesTheInternet checks the path a guest takes off the host: a
// TAP device on the bridge, a default route through the gateway, and NAT on
// the way out.
//
// Nothing below this test can check it. The routing is host configuration,
// the address and route are handed to the guest on its config disk, and only
// a booted guest can tell whether the combination actually carries a packet.
func TestNetworkGuestReachesTheInternet(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	env.startInstance(t, name)

	// The gateway is the bridge on the host, so this half is routing
	// without NAT: it fails if the TAP is not on the bridge.
	if _, err := env.tryExec(t, name, "ping", "-c", "2", "-W", "5", gatewayIP); err != nil {
		t.Errorf("the guest cannot reach its gateway %s: %v", gatewayIP, err)
	}

	// And this half needs NAT, since the guest's address is not routable
	// beyond the host.
	if _, err := env.tryExec(t, name, "ping", "-c", "2", "-W", "5", "8.8.8.8"); err != nil {
		t.Errorf("the guest cannot reach the internet: %v", err)
	}

	// Nameservers reach the guest on the config disk, so this checks
	// dicer-init wrote a resolv.conf that works.
	if _, err := env.tryExec(t, name, "nslookup", "example.com"); err != nil {
		t.Errorf("the guest cannot resolve a name: %v", err)
	}
}

// TestNetworkRulesOfPrefixedBridges runs guests on two networks whose bridge
// names share a prefix -- dicer-e2e and dicer-e2e-b -- and checks that each
// keeps its own forwarding and NAT: setting one up must not mistake the
// other's rules for its own, and tearing one down must not take the other's.
func TestNetworkRulesOfPrefixedBridges(t *testing.T) {
	const other = networkName + "-b"
	env.dicer(t, "network", "create", other, "--subnet", "172.31.1.0/24")
	t.Cleanup(func() { env.deleteNetwork(t, other) })

	onOther := instanceName(t) + "-other"
	env.createInstance(t, onOther, "--network", other)
	env.startInstance(t, onOther)

	onShared := instanceName(t) + "-shared"
	env.createInstance(t, onShared)
	env.startInstance(t, onShared)

	for _, name := range []string{onOther, onShared} {
		if _, err := env.tryExec(t, name, "ping", "-c", "2", "-W", "5", "8.8.8.8"); err != nil {
			t.Errorf("%s cannot reach the internet: %v", name, err)
		}
	}

	// Stopping the only instance on the shared network tears its bridge
	// and rules down, and must leave the other network's alone.
	env.dicer(t, "instance", "stop", onShared)
	env.waitForState(t, onShared, "Stopped")

	if _, err := env.tryExec(t, onOther, "ping", "-c", "2", "-W", "5", "8.8.8.8"); err != nil {
		t.Errorf("%s lost the internet when another network's bridge went: %v", onOther, err)
	}
}

// deleteNetwork removes a network, logging rather than failing so that it
// can be used for cleanup. Registered before a test creates its instances, it
// runs after their cleanups have deleted them.
func (e *environment) deleteNetwork(t *testing.T, name string) {
	t.Helper()

	ctx, cancel := cleanupContext()
	defer cancel()

	if _, err := e.runDicer(ctx, "network", "delete", name); err != nil && !isNotFound(err) {
		t.Logf("cleanup: delete network %s: %v", name, err)
	}
}

// TestNetworkAddressIsReleasedOnDelete checks that an address returns to the pool.
//
// Allocations are persistent by design -- an instance keeps its address
// across restarts -- so the only thing that returns one is deleting the
// instance. If that leaks, a host slowly runs out of subnet.
func TestNetworkAddressIsReleasedOnDelete(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	running := env.startInstance(t, name)

	if !env.hasAllocation(t, running.IP) {
		t.Fatalf("address %s is not listed as allocated while the instance runs", running.IP)
	}

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")

	// A stopped instance keeps its address: that is what makes it the same
	// instance when it starts again.
	if !env.hasAllocation(t, running.IP) {
		t.Errorf("address %s was released by a stop; it should survive one", running.IP)
	}

	env.dicer(t, "instance", "delete", name, "--force")

	if env.hasAllocation(t, running.IP) {
		t.Errorf("address %s is still allocated after the instance was deleted", running.IP)
	}
}

// allocationView is a row of `dicer network allocation list --format json`.
type allocationView struct {
	Instance string `json:"Instance"`
	IP       string `json:"IP"`
	TAP      string `json:"TAP"`
}

// allocations lists the addresses handed out on a network.
func (e *environment) allocations(t *testing.T, network string) []allocationView {
	t.Helper()

	out := e.dicer(t, "network", "allocation", "list", network, "--format", "json")

	return rows[allocationView](t, out, "allocations on "+network)
}

// hasAllocation reports whether an address is currently handed out.
func (e *environment) hasAllocation(t *testing.T, ip string) bool {
	t.Helper()

	return slices.ContainsFunc(e.allocations(t, networkName), func(a allocationView) bool {
		return a.IP == ip
	})
}
