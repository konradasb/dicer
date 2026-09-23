// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package filestore

import (
	"path/filepath"

	"github.com/konradasb/dicer/internal/types"
)

// Typed wrappers over the collections. Lookups accept a name or an ID.

// --- Instances ---

// CreateInstance records a new instance definition.
func (m *Manager) CreateInstance(v types.InstanceSpec) error {
	return m.instances.create(v)
}

// GetInstance returns an instance by name or ID.
func (m *Manager) GetInstance(nameOrID string) (types.InstanceSpec, error) {
	return m.instances.get(nameOrID)
}

// UpdateInstance overwrites an existing instance definition.
func (m *Manager) UpdateInstance(v types.InstanceSpec) error {
	return m.instances.update(v)
}

// RenameInstance moves an instance to a new name, taking its directory --
// its overlay disk, its console log and its snapshots -- with it.
func (m *Manager) RenameInstance(nameOrID string, renamed types.InstanceSpec) error {
	return m.instances.rename(nameOrID, renamed)
}

// DeleteInstance removes an instance and its directory, including its overlay disk.
func (m *Manager) DeleteInstance(nameOrID string) error {
	return m.instances.delete(nameOrID)
}

// ListInstances returns every instance, sorted by name.
func (m *Manager) ListInstances() ([]types.InstanceSpec, error) {
	return m.instances.list(), nil
}

// InstanceDir returns an instance's persistent directory, removed when the
// instance is deleted.
func (m *Manager) InstanceDir(name string) string {
	return filepath.Join(m.dataDir, instancesDir, name)
}

// --- Networks ---

// CreateNetwork records a new network definition.
func (m *Manager) CreateNetwork(v types.Network) error {
	return m.networks.create(v)
}

// GetNetwork returns a network by name or ID.
func (m *Manager) GetNetwork(nameOrID string) (types.Network, error) {
	return m.networks.get(nameOrID)
}

// UpdateNetwork overwrites an existing network definition.
func (m *Manager) UpdateNetwork(v types.Network) error {
	return m.networks.update(v)
}

// DeleteNetwork removes a network definition.
func (m *Manager) DeleteNetwork(nameOrID string) error {
	return m.networks.delete(nameOrID)
}

// ListNetworks returns every network, sorted by name.
func (m *Manager) ListNetworks() ([]types.Network, error) {
	return m.networks.list(), nil
}

// --- Volumes ---

// CreateVolume records a new volume definition.
func (m *Manager) CreateVolume(v types.Volume) error {
	return m.volumes.create(v)
}

// GetVolume returns a volume by name or ID.
func (m *Manager) GetVolume(nameOrID string) (types.Volume, error) {
	return m.volumes.get(nameOrID)
}

// UpdateVolume overwrites an existing volume definition.
func (m *Manager) UpdateVolume(v types.Volume) error {
	return m.volumes.update(v)
}

// DeleteVolume removes a volume definition. The backing disk is the volume store's to delete.
func (m *Manager) DeleteVolume(nameOrID string) error {
	return m.volumes.delete(nameOrID)
}

// ListVolumes returns every volume, sorted by name.
func (m *Manager) ListVolumes() ([]types.Volume, error) {
	return m.volumes.list(), nil
}

// --- Kernels ---

// CreateKernel records a new kernel definition.
func (m *Manager) CreateKernel(v types.Kernel) error {
	return m.kernels.create(v)
}

// GetKernel returns a kernel by name or ID.
func (m *Manager) GetKernel(nameOrID string) (types.Kernel, error) {
	return m.kernels.get(nameOrID)
}

// DeleteKernel removes a kernel definition. The binary is the kernel store's to delete.
func (m *Manager) DeleteKernel(nameOrID string) error {
	return m.kernels.delete(nameOrID)
}

// ListKernels returns every kernel, sorted by name.
func (m *Manager) ListKernels() ([]types.Kernel, error) {
	return m.kernels.list(), nil
}
