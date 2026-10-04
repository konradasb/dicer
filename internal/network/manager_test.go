// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package network

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
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

	allocations, err := a.List(n.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(allocations) != 0 {
		t.Errorf("got %d allocations after release, want 0", len(allocations))
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

	allocations, err := a.List(n.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(allocations) != 1 || allocations[0].InstanceID != "id-web" {
		t.Errorf("remaining allocations = %v, want just id-web", allocations)
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

func TestForgetDiscardsTheWholeAllocationTable(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()

	if _, err := a.Allocate(n, "id-web", ""); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if err := a.Forget(n.Name); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	// Forgetting a network with no allocation table is not an error.
	if err := a.Forget(n.Name); err != nil {
		t.Fatalf("second Forget: %v", err)
	}

	allocations, err := a.List(n.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(allocations) != 0 {
		t.Errorf("got %d allocations after Forget, want 0", len(allocations))
	}
}

func TestListUnknownNetworkIsEmptyNotAnError(t *testing.T) {
	a := newTestManager(t)

	allocations, err := a.List("never-created")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(allocations) != 0 {
		t.Errorf("got %d allocations, want 0", len(allocations))
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

func TestInstanceAtIsTheOneHoldingTheAddress(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()
	// A fixed address, so that the free one below cannot be the one picked.
	web, err := a.Allocate(n, "id-web", "10.0.0.5")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	tests := []struct {
		name, network, ip string
		want              string
		wantOK            bool
	}{
		{"held", n.Name, web.IP, "id-web", true},
		{"free", n.Name, "10.0.0.200", "", false},
		{"the gateway", n.Name, n.Gateway, "", false},
		{"unknown network", "never-created", web.IP, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := a.InstanceAt(tt.network, tt.ip)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("InstanceAt(%q, %q) = %q, %v; want %q, %v", tt.network, tt.ip, got, ok, tt.want, tt.wantOK)
			}
		})
	}

	if err := a.Release(n.Name, "id-web"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got, ok := a.InstanceAt(n.Name, web.IP); ok {
		t.Errorf("InstanceAt after release = %q, want none", got)
	}
}

// What a caller does with the allocations List returns is its own business.
func TestListReturnsACopy(t *testing.T) {
	a := newTestManager(t)
	n := testNetwork()
	want, err := a.Allocate(n, "id-web", "")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	allocations, err := a.List(n.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	allocations[0].IP = "10.0.0.99"

	if got, err := a.Get(n.Name, "id-web"); err != nil || got.IP != want.IP {
		t.Errorf("Get after changing List's result = %v, %v; want %s", got.IP, err, want.IP)
	}
}

// An allocation that could not be saved is not handed out, nor remembered.
func TestFailedSaveChangesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "allocations")
	a, err := NewManager(Config{Dir: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	n := testNetwork()
	if _, err := a.Allocate(n, "id-web", ""); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	// A file where the directory was: nothing can be written there, even
	// by root.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Allocate(n, "id-db", ""); err == nil {
		t.Fatal("Allocate with nowhere to save succeeded")
	}
	if _, err := a.Get(n.Name, "id-db"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("Get of the unsaved allocation = %v, want not found", err)
	}
	if err := a.Release(n.Name, "id-web"); err == nil {
		t.Fatal("Release with nowhere to save succeeded")
	}
	if _, err := a.Get(n.Name, "id-web"); err != nil {
		t.Errorf("Get after the release failed = %v, want the allocation kept", err)
	}
}

// An allocation table that cannot be read fails its own network's calls,
// and no other's.
func TestUnreadableAllocationTableFailsOnlyItsNetwork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "allocations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("{not: [a table"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := NewManager(Config{Dir: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := a.List("broken"); err == nil {
		t.Error("List of the unreadable allocation table succeeded")
	}
	if _, err := a.Allocate(testNetwork(), "id-web", ""); err != nil {
		t.Errorf("Allocate on another network: %v", err)
	}

	if err := a.Forget("broken"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := a.List("broken"); err != nil {
		t.Errorf("List after Forget: %v", err)
	}
}

// Looking an allocation up costs the same however many the network has.
func BenchmarkGet(b *testing.B) {
	for _, size := range []int{10, 100, 1000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			a, err := NewManager(Config{Dir: b.TempDir()})
			if err != nil {
				b.Fatal(err)
			}
			n := types.Network{ID: "n", Name: "lan", Subnet: "10.0.0.0/16", Gateway: "10.0.0.1"}
			for i := range size {
				if _, err := a.Allocate(n, "id-"+strconv.Itoa(i), ""); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			for b.Loop() {
				if _, err := a.Get(n.Name, "id-5"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
