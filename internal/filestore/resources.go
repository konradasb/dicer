// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package filestore

import (
	"path/filepath"

	"github.com/dicer-sh/dicer"
)

// The methods below are thin typed wrappers over the generic collection.
//
// They deliberately take no context. The store is an in-memory cache with
// synchronous, local file writes: there is nothing to cancel, and a context
// parameter would advertise a cancellation this package does not honour.
//
// Lookups accept either a name or an ID.

// --- Instances ---

// CreateInstance records a new instance definition.
func (m *Manager) CreateInstance(v dicer.InstanceSpec) error {
	return m.instances.create(v)
}

// GetInstance returns an instance by name or ID.
func (m *Manager) GetInstance(nameOrID string) (dicer.InstanceSpec, error) {
	return m.instances.get(nameOrID)
}

// UpdateInstance overwrites an existing instance definition.
func (m *Manager) UpdateInstance(v dicer.InstanceSpec) error {
	return m.instances.update(v)
}

// RenameInstance moves an instance to a new name, taking its directory --
// its overlay disk, its console log and its snapshots -- with it.
func (m *Manager) RenameInstance(nameOrID string, renamed dicer.InstanceSpec) error {
	return m.instances.rename(nameOrID, renamed)
}

// DeleteInstance removes an instance and its directory, including its overlay disk.
func (m *Manager) DeleteInstance(nameOrID string) error {
	return m.instances.delete(nameOrID)
}

// ListInstances returns every instance, sorted by name.
func (m *Manager) ListInstances() ([]dicer.InstanceSpec, error) {
	return m.instances.list(), nil
}

// InstanceDir returns the persistent directory owned by an instance. It holds
// the definition and the writable overlay disk, so both survive a host
// reboot, and both are removed when the instance is deleted. Ephemeral state
// belongs under the runtime directory instead, which internal/vm owns.
func (m *Manager) InstanceDir(name string) string {
	return filepath.Join(m.dataDir, instancesDir, name)
}

// --- Networks ---

// CreateNetwork records a new network definition.
func (m *Manager) CreateNetwork(v dicer.Network) error {
	return m.networks.create(v)
}

// GetNetwork returns a network by name or ID.
func (m *Manager) GetNetwork(nameOrID string) (dicer.Network, error) {
	return m.networks.get(nameOrID)
}

// UpdateNetwork overwrites an existing network definition.
func (m *Manager) UpdateNetwork(v dicer.Network) error {
	return m.networks.update(v)
}

// DeleteNetwork removes a network definition.
func (m *Manager) DeleteNetwork(nameOrID string) error {
	return m.networks.delete(nameOrID)
}

// ListNetworks returns every network, sorted by name.
func (m *Manager) ListNetworks() ([]dicer.Network, error) {
	return m.networks.list(), nil
}

// --- Volumes ---

// CreateVolume records a new volume definition.
func (m *Manager) CreateVolume(v dicer.Volume) error {
	return m.volumes.create(v)
}

// GetVolume returns a volume by name or ID.
func (m *Manager) GetVolume(nameOrID string) (dicer.Volume, error) {
	return m.volumes.get(nameOrID)
}

// UpdateVolume overwrites an existing volume definition.
func (m *Manager) UpdateVolume(v dicer.Volume) error {
	return m.volumes.update(v)
}

// DeleteVolume removes a volume definition. The backing disk is the volume store's to delete.
func (m *Manager) DeleteVolume(nameOrID string) error {
	return m.volumes.delete(nameOrID)
}

// ListVolumes returns every volume, sorted by name.
func (m *Manager) ListVolumes() ([]dicer.Volume, error) {
	return m.volumes.list(), nil
}

// --- Kernels ---

// CreateKernel records a new kernel definition.
func (m *Manager) CreateKernel(v dicer.Kernel) error {
	return m.kernels.create(v)
}

// GetKernel returns a kernel by name or ID.
func (m *Manager) GetKernel(nameOrID string) (dicer.Kernel, error) {
	return m.kernels.get(nameOrID)
}

// DeleteKernel removes a kernel definition. The binary is the kernel store's to delete.
func (m *Manager) DeleteKernel(nameOrID string) error {
	return m.kernels.delete(nameOrID)
}

// ListKernels returns every kernel, sorted by name.
func (m *Manager) ListKernels() ([]dicer.Kernel, error) {
	return m.kernels.list(), nil
}

// --- Trusted clients ---

// CreateClient records a newly trusted client.
func (m *Manager) CreateClient(v dicer.TrustedClient) error {
	return m.clients.create(v)
}

// GetClient returns a trusted client by name or certificate fingerprint.
func (m *Manager) GetClient(nameOrFingerprint string) (dicer.TrustedClient, error) {
	return m.clients.get(nameOrFingerprint)
}

// DeleteClient stops trusting a client.
func (m *Manager) DeleteClient(nameOrFingerprint string) error {
	return m.clients.delete(nameOrFingerprint)
}

// ListClients returns every trusted client, sorted by name.
func (m *Manager) ListClients() ([]dicer.TrustedClient, error) {
	return m.clients.list(), nil
}

// --- Enrolment tokens ---

// CreateToken records an outstanding enrolment token.
func (m *Manager) CreateToken(v dicer.AccessToken) error {
	return m.tokens.create(v)
}

// GetToken returns an enrolment token by name or secret hash.
func (m *Manager) GetToken(nameOrSecretHash string) (dicer.AccessToken, error) {
	return m.tokens.get(nameOrSecretHash)
}

// DeleteToken removes an enrolment token.
func (m *Manager) DeleteToken(nameOrSecretHash string) error {
	return m.tokens.delete(nameOrSecretHash)
}

// ListTokens returns every outstanding enrolment token, sorted by name.
func (m *Manager) ListTokens() ([]dicer.AccessToken, error) {
	return m.tokens.list(), nil
}
