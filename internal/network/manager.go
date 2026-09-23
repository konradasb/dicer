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

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// Manager assigns addresses from a subnet and persists the allocations, so an
// instance keeps its address across restarts. Allocations are released on
// delete and reconciled at daemon start.
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
func (m *Manager) read(network string) ([]types.NetworkAllocation, error) {
	data, err := os.ReadFile(m.path(network))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read allocations for %q: %w", network, err)
	}

	var allocs []types.NetworkAllocation
	if err := yaml.Unmarshal(data, &allocs); err != nil {
		return nil, fmt.Errorf("parse allocations for %q: %w", network, err)
	}
	return allocs, nil
}

// write saves a network's table. Must be called with the lock held.
func (m *Manager) write(network string, allocs []types.NetworkAllocation) error {
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
func (m *Manager) List(network string) ([]types.NetworkAllocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.read(network)
}

// Get returns the allocation held by an instance on a network.
func (m *Manager) Get(network, instanceID string) (types.NetworkAllocation, error) {
	allocs, err := m.List(network)
	if err != nil {
		return types.NetworkAllocation{}, err
	}

	for _, alloc := range allocs {
		if alloc.InstanceID == instanceID {
			return alloc, nil
		}
	}

	return types.NetworkAllocation{}, errdefs.NotFound("instance %q has no address on network %q", instanceID, network)
}

// Allocate assigns an address to an instance, or returns the one it holds. A
// staticIP must be in the subnet and free.
func (m *Manager) Allocate(n types.Network, instanceID, staticIP string) (types.NetworkAllocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	allocs, err := m.read(n.Name)
	if err != nil {
		return types.NetworkAllocation{}, err
	}

	for _, alloc := range allocs {
		if alloc.InstanceID == instanceID {
			return alloc, nil
		}
	}

	_, ipNet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return types.NetworkAllocation{}, fmt.Errorf("invalid subnet %q: %w", n.Subnet, err)
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
			return types.NetworkAllocation{}, errdefs.InvalidArgument(
				"static IP %q is not an assignable address in subnet %s", staticIP, n.Subnet)
		}
		ip = parsed.To4().String()
		if _, taken := used[ip]; taken {
			return types.NetworkAllocation{}, errdefs.InvalidState(
				"static IP %s is already in use on network %q", ip, n.Name)
		}
	} else {
		ip, err = allocateIP(ipNet, used)
		if err != nil {
			return types.NetworkAllocation{}, fmt.Errorf(
				"allocate address on network %q: %w", n.Name, err)
		}
	}

	mac, err := randomMAC()
	if err != nil {
		return types.NetworkAllocation{}, fmt.Errorf("generate MAC: %w", err)
	}

	alloc := types.NetworkAllocation{
		NetworkID:  n.ID,
		InstanceID: instanceID,
		IP:         ip,
		MAC:        mac,
	}

	if err := m.write(n.Name, append(allocs, alloc)); err != nil {
		return types.NetworkAllocation{}, err
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

	kept := make([]types.NetworkAllocation, 0, len(allocs))
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

// Reconcile drops allocations on the given networks held by instances not in
// live, and returns how many it released.
func (m *Manager) Reconcile(networks []string, live map[string]struct{}) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var released int
	for _, network := range networks {
		allocs, err := m.read(network)
		if err != nil {
			return released, err
		}

		kept := make([]types.NetworkAllocation, 0, len(allocs))
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
