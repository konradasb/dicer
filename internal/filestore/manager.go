// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package filestore stores the instance, snapshot, network, volume and
// kernel definitions as YAML files, cached in memory and written through:
//
//	/var/lib/dicer/instances/<name>/config.yaml
//	/var/lib/dicer/snapshots/<name>/config.yaml
//	/var/lib/dicer/networks/<name>.yaml
//	/var/lib/dicer/volumes/<name>.yaml
//	/var/lib/dicer/kernels/<name>.yaml
package filestore

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/konradasb/dicer/internal/defaults"
	"github.com/konradasb/dicer/internal/types"
)

// Config configures a Manager.
type Config struct {
	// DataDir holds the definitions; empty means defaults.DataDir.
	DataDir string
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// Manager holds the definitions, backed by YAML files. It implements
// vm.Definitions and is safe for concurrent use.
//
// Every lookup takes a name or an ID and returns an errdefs.ErrNotFound error
// if there is no such definition.
type Manager struct {
	instances *collection[types.InstanceSpec]
	snapshots *collection[types.Snapshot]
	networks  *collection[types.Network]
	volumes   *collection[types.Volume]
	kernels   *collection[types.Kernel]
}

// NewManager loads all definitions into memory, creating their directories
// if needed. Malformed files are logged and skipped.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = defaults.DataDir
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	logger := cfg.Logger.With("component", "filestore")

	m := &Manager{
		instances: newCollection(
			"instance", filepath.Join(cfg.DataDir, "instances"), nested, logger,
			func(v types.InstanceSpec) (string, string) { return v.ID, v.Name },
		),
		snapshots: newCollection(
			"snapshot", filepath.Join(cfg.DataDir, "snapshots"), nested, logger,
			func(v types.Snapshot) (string, string) { return v.ID, v.Name },
		),
		networks: newCollection(
			"network", filepath.Join(cfg.DataDir, "networks"), flat, logger,
			func(v types.Network) (string, string) { return v.ID, v.Name },
		),
		volumes: newCollection(
			"volume", filepath.Join(cfg.DataDir, "volumes"), flat, logger,
			func(v types.Volume) (string, string) { return v.ID, v.Name },
		),
		kernels: newCollection(
			"kernel", filepath.Join(cfg.DataDir, "kernels"), flat, logger,
			func(v types.Kernel) (string, string) { return v.ID, v.Name },
		),
	}
	for _, load := range []func() error{
		m.instances.load, m.snapshots.load, m.networks.load, m.volumes.load, m.kernels.load,
	} {
		if err := load(); err != nil {
			return nil, err
		}
	}

	logger.Info("definitions loaded",
		"data_dir", cfg.DataDir,
		"instances", m.instances.len(),
		"snapshots", m.snapshots.len(),
		"networks", m.networks.len(),
		"volumes", m.volumes.len(),
		"kernels", m.kernels.len(),
	)

	return m, nil
}

// CreateInstance records a new instance definition.
func (m *Manager) CreateInstance(v types.InstanceSpec) error {
	return m.instances.create(v)
}

// Instance returns an instance by name or ID.
func (m *Manager) Instance(nameOrID string) (types.InstanceSpec, error) {
	return m.instances.definition(nameOrID)
}

// UpdateInstance overwrites an existing instance definition.
func (m *Manager) UpdateInstance(v types.InstanceSpec) error {
	return m.instances.update(v)
}

// RenameInstance moves an instance to a new name, taking its directory, with
// its overlay disk, console log and snapshots, with it.
func (m *Manager) RenameInstance(nameOrID string, renamed types.InstanceSpec) error {
	return m.instances.rename(nameOrID, renamed)
}

// DeleteInstance removes an instance and its directory, including its
// overlay disk.
func (m *Manager) DeleteInstance(nameOrID string) error {
	return m.instances.delete(nameOrID)
}

// Instances returns every instance, sorted by name.
func (m *Manager) Instances() []types.InstanceSpec {
	return m.instances.definitions()
}

// MatchingInstances returns the instances match reports true for, in no
// particular order. match must not call back into the Manager.
func (m *Manager) MatchingInstances(match func(types.InstanceSpec) bool) []types.InstanceSpec {
	return m.instances.matchingDefinitions(match)
}

// InstanceDir returns an instance's persistent directory, removed when the
// instance is deleted.
func (m *Manager) InstanceDir(name string) string {
	return m.instances.nestedDir(name)
}

// StageSnapshot returns a new, empty directory beside the snapshots for a
// snapshot's files to be written in before CreateSnapshot moves it into
// place. One a crash leaves behind is removed when the store next loads.
func (m *Manager) StageSnapshot() (string, error) {
	dir, err := os.MkdirTemp(m.snapshots.dir, stagingPrefix)
	if err != nil {
		return "", fmt.Errorf("create snapshot staging directory: %w", err)
	}
	return dir, nil
}

// CreateSnapshot records a new snapshot whose files are in staged, a
// directory from StageSnapshot, moving it into place. A snapshot is thus
// either whole or absent.
func (m *Manager) CreateSnapshot(v types.Snapshot, staged string) error {
	return m.snapshots.createFrom(v, staged)
}

// Snapshot returns a snapshot by name or ID.
func (m *Manager) Snapshot(nameOrID string) (types.Snapshot, error) {
	return m.snapshots.definition(nameOrID)
}

// Snapshots returns every snapshot, sorted by name.
func (m *Manager) Snapshots() []types.Snapshot {
	return m.snapshots.definitions()
}

// DeleteSnapshot removes a snapshot and its files.
func (m *Manager) DeleteSnapshot(nameOrID string) error {
	return m.snapshots.delete(nameOrID)
}

// SnapshotDir returns the directory holding a snapshot's files.
func (m *Manager) SnapshotDir(name string) string {
	return m.snapshots.nestedDir(name)
}

// CreateNetwork records a new network definition.
func (m *Manager) CreateNetwork(v types.Network) error {
	return m.networks.create(v)
}

// Network returns a network by name or ID.
func (m *Manager) Network(nameOrID string) (types.Network, error) {
	return m.networks.definition(nameOrID)
}

// DeleteNetwork removes a network definition.
func (m *Manager) DeleteNetwork(nameOrID string) error {
	return m.networks.delete(nameOrID)
}

// Networks returns every network, sorted by name.
func (m *Manager) Networks() []types.Network {
	return m.networks.definitions()
}

// CreateVolume records a new volume definition.
func (m *Manager) CreateVolume(v types.Volume) error {
	return m.volumes.create(v)
}

// Volume returns a volume by name or ID.
func (m *Manager) Volume(nameOrID string) (types.Volume, error) {
	return m.volumes.definition(nameOrID)
}

// DeleteVolume removes a volume definition. The backing disk is the volume
// manager's to delete.
func (m *Manager) DeleteVolume(nameOrID string) error {
	return m.volumes.delete(nameOrID)
}

// Volumes returns every volume, sorted by name.
func (m *Manager) Volumes() []types.Volume {
	return m.volumes.definitions()
}

// CreateKernel records a new kernel definition.
func (m *Manager) CreateKernel(v types.Kernel) error {
	return m.kernels.create(v)
}

// Kernel returns a kernel by name or ID.
func (m *Manager) Kernel(nameOrID string) (types.Kernel, error) {
	return m.kernels.definition(nameOrID)
}

// DeleteKernel removes a kernel definition. The binary is the kernel
// manager's to delete.
func (m *Manager) DeleteKernel(nameOrID string) error {
	return m.kernels.delete(nameOrID)
}

// Kernels returns every kernel, sorted by name.
func (m *Manager) Kernels() []types.Kernel {
	return m.kernels.definitions()
}
