// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package network

import (
	"path/filepath"
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

// The address manager is tested with no store and no daemon: address policy needs
// only a subnet and a set of opaque instance IDs.

func newTestManager(t *testing.T) *Manager {
	t.Helper()

	a, err := NewManager(Config{Dir: filepath.Join(t.TempDir(), "allocations")})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	return a
}

func testNetwork() types.Network {
	return types.Network{
		ID:      "net-default",
		Name:    "default",
		Subnet:  "10.0.0.0/24",
		Gateway: "10.0.0.1",
	}
}

func TestAllocateIsStableAndIdempotent(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()

	first, err := a.Allocate(n, "id-web", "")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	again, err := a.Allocate(n, "id-web", "")
	if err != nil {
		t.Fatalf("second Allocate: %v", err)
	}
	if again.IP != first.IP || again.MAC != first.MAC {
		t.Errorf("re-allocating gave %s/%s, want the existing %s/%s",
			again.IP, again.MAC, first.IP, first.MAC)
	}

	other, err := a.Allocate(n, "id-db", "")
	if err != nil {
		t.Fatalf("Allocate for a second instance: %v", err)
	}
	if other.IP == first.IP {
		t.Errorf("two instances were given the same IP %s", other.IP)
	}
	if other.IP == n.Gateway {
		t.Errorf("allocated the gateway address")
	}
}

func TestAllocatePersists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "allocations")
	n := testNetwork()

	a, err := NewManager(Config{Dir: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	want, err := a.Allocate(n, "id-web", "")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	// A restart must hand back the same address.
	reopened, err := NewManager(Config{Dir: dir})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get(n.Name, "id-web")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.IP != want.IP {
		t.Errorf("IP after reopen = %s, want %s", got.IP, want.IP)
	}
}

func TestAllocateStaticIP(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()

	got, err := a.Allocate(n, "id-web", "10.0.0.50")
	if err != nil {
		t.Fatalf("Allocate static: %v", err)
	}
	if got.IP != "10.0.0.50" {
		t.Errorf("IP = %s, want 10.0.0.50", got.IP)
	}

	if _, err := a.Allocate(n, "id-db", "10.0.0.50"); err == nil {
		t.Error("allocating an already-taken static IP should fail")
	}
	if _, err := a.Allocate(n, "id-db", "192.168.1.1"); err == nil {
		t.Error("allocating a static IP outside the subnet should fail")
	}
	if _, err := a.Allocate(n, "id-db", "not-an-ip"); err == nil {
		t.Error("allocating a malformed static IP should fail")
	}
}

func TestAllocateStaticIPCannotTakeTheGateway(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()

	if _, err := a.Allocate(n, "id-web", n.Gateway); err == nil {
		t.Error("allocating the gateway address should fail")
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	a := newTestManager(t)

	if _, err := a.Get("default", "id-nope"); err == nil {
		t.Error("Get for an unallocated instance should fail")
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()

	if _, err := a.Allocate(n, "id-web", ""); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	for range 2 {
		if err := a.Release(n.Name, "id-web"); err != nil {
			t.Fatalf("Release: %v", err)
		}
	}

	allocs, err := a.List(n.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(allocs) != 0 {
		t.Errorf("got %d allocations after release, want 0", len(allocs))
	}
}

func TestReconcileDropsOrphans(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()

	if _, err := a.Allocate(n, "id-web", ""); err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	// An address leaked by an unclean shutdown: allocated, but its instance
	// is gone.
	if _, err := a.Allocate(n, "id-ghost", ""); err != nil {
		t.Fatalf("Allocate ghost: %v", err)
	}

	live := map[string]struct{}{"id-web": {}}
	released, err := a.Reconcile([]string{n.Name}, live)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if released != 1 {
		t.Errorf("released %d allocations, want 1", released)
	}

	allocs, err := a.List(n.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(allocs) != 1 || allocs[0].InstanceID != "id-web" {
		t.Errorf("remaining allocations = %v, want just id-web", allocs)
	}
}

func TestReconcileIsANoopWhenNothingIsOrphaned(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()

	if _, err := a.Allocate(n, "id-web", ""); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	released, err := a.Reconcile([]string{n.Name}, map[string]struct{}{"id-web": {}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if released != 0 {
		t.Errorf("released %d allocations, want 0", released)
	}
}

func TestForgetDiscardsTheWholeTable(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()

	if _, err := a.Allocate(n, "id-web", ""); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if err := a.Forget(n.Name); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	// Forgetting a network with no table is not an error.
	if err := a.Forget(n.Name); err != nil {
		t.Fatalf("second Forget: %v", err)
	}

	allocs, err := a.List(n.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(allocs) != 0 {
		t.Errorf("got %d allocations after Forget, want 0", len(allocs))
	}
}

func TestListUnknownNetworkIsEmptyNotAnError(t *testing.T) {
	a := newTestManager(t)

	allocs, err := a.List("never-created")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(allocs) != 0 {
		t.Errorf("got %d allocations, want 0", len(allocs))
	}
}

func TestExhaustedSubnet(t *testing.T) {
	a := newTestManager(t)
	// A /30 has 4 addresses: network, gateway, one host, broadcast.
	n := types.Network{ID: "n", Name: "tiny", Subnet: "10.9.0.0/30", Gateway: "10.9.0.1"}

	if _, err := a.Allocate(n, "id-1", ""); err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	if _, err := a.Allocate(n, "id-2", ""); err == nil {
		t.Error("allocating from an exhausted subnet should fail")
	}
}
