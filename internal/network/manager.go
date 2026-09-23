// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package network

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/atomicfile"
)

// Manager assigns addresses from a subnet and remembers what it handed out.
//
// Allocations are persistent, so an instance keeps the same address across
// stop/start and across host reboots. They are released when an instance is
// deleted, and reconciled against the set of live instances at daemon start,
// so a crash cannot leak an address permanently.
//
// The Manager owns its own table, in its own directory. It deliberately
// does not know what an instance is beyond an opaque ID, and does not consult
// the definition store: Reconcile is *told* which IDs are live rather than
// going to look, which keeps address policy independent of storage.
type Manager struct {
	dir    string
	logger *slog.Logger

	// mu guards the per-network tables. Assignment is a read-modify-write
	// over a whole network, so one lock across all of them is both correct
	// and, at the scale of one host, cheap.
	mu sync.Mutex
}

// Config configures a Manager.
type Config struct {
	// Dir is the directory the allocation tables are kept in.
	Dir string

	Logger *slog.Logger
}

// NewManager returns a Manager storing its tables under cfg.Dir.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", cfg.Dir, err)
	}

	return &Manager{dir: cfg.Dir, logger: cfg.Logger.With("component", "network")}, nil
}

func (m *Manager) path(network string) string {
	return filepath.Join(m.dir, network+".yaml")
}

// read loads a network's table. Must be called with the lock held.
func (m *Manager) read(network string) ([]dicer.NetworkAllocation, error) {
	data, err := os.ReadFile(m.path(network))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read allocations for %q: %w", network, err)
	}

	var allocs []dicer.NetworkAllocation
	if err := yaml.Unmarshal(data, &allocs); err != nil {
		return nil, fmt.Errorf("parse allocations for %q: %w", network, err)
	}
	return allocs, nil
}

// write saves a network's table. Must be called with the lock held.
func (m *Manager) write(network string, allocs []dicer.NetworkAllocation) error {
	data, err := yaml.Marshal(allocs)
	if err != nil {
		return fmt.Errorf("marshal allocations for %q: %w", network, err)
	}

	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", m.dir, err)
	}

	return atomicfile.Write(m.path(network), data, 0o600)
}

// List returns every allocation on a network.
func (m *Manager) List(network string) ([]dicer.NetworkAllocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.read(network)
}

// Get returns the allocation held by an instance on a network.
func (m *Manager) Get(network, instanceID string) (dicer.NetworkAllocation, error) {
	allocs, err := m.List(network)
	if err != nil {
		return dicer.NetworkAllocation{}, err
	}

	for _, alloc := range allocs {
		if alloc.InstanceID == instanceID {
			return alloc, nil
		}
	}

	return dicer.NetworkAllocation{}, dicer.NotFound("instance %q has no address on network %q", instanceID, network)
}

// Allocate assigns an address to an instance, or returns the existing one if
// the instance already holds it -- so starting an already-allocated instance
// is idempotent and address-stable.
//
// If staticIP is set it is used verbatim, failing if it is outside the subnet
// or already taken by a different instance.
func (m *Manager) Allocate(n dicer.Network, instanceID, staticIP string) (dicer.NetworkAllocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	allocs, err := m.read(n.Name)
	if err != nil {
		return dicer.NetworkAllocation{}, err
	}

	for _, alloc := range allocs {
		if alloc.InstanceID == instanceID {
			return alloc, nil
		}
	}

	_, ipNet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return dicer.NetworkAllocation{}, fmt.Errorf("invalid subnet %q: %w", n.Subnet, err)
	}

	used := make(map[string]struct{}, len(allocs)+1)
	used[n.Gateway] = struct{}{}
	for _, alloc := range allocs {
		used[alloc.IP] = struct{}{}
	}

	var ip string
	if staticIP != "" {
		parsed := net.ParseIP(staticIP)
		if parsed == nil || !Assignable(ipNet, parsed) {
			return dicer.NetworkAllocation{}, dicer.InvalidArgument(
				"static IP %q is not an assignable address in subnet %s", staticIP, n.Subnet)
		}
		ip = parsed.To4().String()
		if _, taken := used[ip]; taken {
			return dicer.NetworkAllocation{}, dicer.InvalidState(
				"static IP %s is already in use on network %q", ip, n.Name)
		}
	} else {
		ip, err = allocateIP(ipNet, used)
		if err != nil {
			return dicer.NetworkAllocation{}, fmt.Errorf(
				"allocate address on network %q: %w", n.Name, err)
		}
	}

	mac, err := randomMAC()
	if err != nil {
		return dicer.NetworkAllocation{}, fmt.Errorf("generate MAC: %w", err)
	}

	alloc := dicer.NetworkAllocation{
		NetworkID:  n.ID,
		InstanceID: instanceID,
		IP:         ip,
		MAC:        mac,
	}

	if err := m.write(n.Name, append(allocs, alloc)); err != nil {
		return dicer.NetworkAllocation{}, err
	}

	return alloc, nil
}

// Release drops an instance's allocation. Releasing one that does not exist
// is not an error, so cleanup paths can call it unconditionally.
func (m *Manager) Release(network, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	allocs, err := m.read(network)
	if err != nil {
		return err
	}

	kept := make([]dicer.NetworkAllocation, 0, len(allocs))
	for _, alloc := range allocs {
		if alloc.InstanceID != instanceID {
			kept = append(kept, alloc)
		}
	}
	if len(kept) == len(allocs) {
		return nil
	}

	return m.write(network, kept)
}

// Forget discards a network's whole table, for use when the network itself is
// deleted.
func (m *Manager) Forget(network string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.Remove(m.path(network)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove allocations for %q: %w", network, err)
	}

	return nil
}

// Reconcile drops allocations held by instances that are no longer live,
// across the given networks, and reports how many it released.
//
// The caller supplies the live set: this package has no opinion about what
// makes an instance live, and no way to find out. That is the lifecycle
// layer's business.
func (m *Manager) Reconcile(networks []string, live map[string]struct{}) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var released int
	for _, network := range networks {
		allocs, err := m.read(network)
		if err != nil {
			return released, err
		}

		kept := make([]dicer.NetworkAllocation, 0, len(allocs))
		for _, alloc := range allocs {
			if _, ok := live[alloc.InstanceID]; ok {
				kept = append(kept, alloc)
				continue
			}
			released++
			m.logger.Info("releasing orphaned allocation",
				"network", network, "instance_id", alloc.InstanceID, "ip", alloc.IP)
		}

		if len(kept) == len(allocs) {
			continue
		}
		if err := m.write(network, kept); err != nil {
			return released, err
		}
	}

	return released, nil
}
