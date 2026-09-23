// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"path/filepath"

	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// An instance's files live in two places: the persistent instance directory,
// keyed by name, holds the overlay disk, console log and snapshots; the
// runtime directory under RunDir, keyed by ID, holds the runtime state,
// sockets, config and status disks and the VMM's log.
const (
	overlayDiskFile      = "overlay.img"
	serialLogFile        = "serial.log"
	snapshotsDirName     = "snapshots"
	runtimeStateFile     = "state.json"
	configDiskFile       = "config.img"
	statusDiskFile       = "status.img"
	hypervisorSocketFile = "hypervisor.sock"
	vsockSocketFile      = "vsock.sock"
	snapshotMetadataFile = "snapshot.json"
)

// instanceDir returns an instance's persistent directory.
func (m *Manager) instanceDir(inst types.InstanceSpec) string {
	return m.definitions.InstanceDir(inst.Name)
}

// overlayDiskPath returns the writable disk holding an instance's root
// filesystem.
func (m *Manager) overlayDiskPath(inst types.InstanceSpec) string {
	return filepath.Join(m.instanceDir(inst), overlayDiskFile)
}

// serialLogPath returns the file an instance's serial console is written to.
func (m *Manager) serialLogPath(inst types.InstanceSpec) string {
	return filepath.Join(m.instanceDir(inst), serialLogFile)
}

// snapshotsDir returns the directory holding an instance's snapshots.
func (m *Manager) snapshotsDir(inst types.InstanceSpec) string {
	return filepath.Join(m.instanceDir(inst), snapshotsDirName)
}

// snapshotDir returns the directory holding one snapshot.
func (m *Manager) snapshotDir(inst types.InstanceSpec, name string) string {
	return filepath.Join(m.snapshotsDir(inst), name)
}

// snapshotMetadataPath returns the file recording a snapshot's metadata.
func (m *Manager) snapshotMetadataPath(inst types.InstanceSpec, name string) string {
	return filepath.Join(m.snapshotDir(inst, name), snapshotMetadataFile)
}

// snapshotOverlayDiskPath returns a snapshot's copy of the overlay disk.
func (m *Manager) snapshotOverlayDiskPath(inst types.InstanceSpec, name string) string {
	return filepath.Join(m.snapshotDir(inst, name), overlayDiskFile)
}

// runtimeDir returns an instance's ephemeral directory.
func (m *Manager) runtimeDir(instanceID string) string {
	return filepath.Join(m.runDir, "instances", instanceID)
}

// runtimeStatePath returns the file an instance's runtime state is kept in.
func (m *Manager) runtimeStatePath(instanceID string) string {
	return filepath.Join(m.runtimeDir(instanceID), runtimeStateFile)
}

// configDiskPath returns the disk dicer-init reads its configuration from.
func (m *Manager) configDiskPath(instanceID string) string {
	return filepath.Join(m.runtimeDir(instanceID), configDiskFile)
}

// statusDiskPath returns the disk the guest reports its end on.
func (m *Manager) statusDiskPath(instanceID string) string {
	return filepath.Join(m.runtimeDir(instanceID), statusDiskFile)
}

// hypervisorSocketPath returns the socket an instance's VMM serves its API on.
func (m *Manager) hypervisorSocketPath(instanceID string) string {
	return filepath.Join(m.runtimeDir(instanceID), hypervisorSocketFile)
}

// hypervisorLogPath returns the VMM's own log.
func (m *Manager) hypervisorLogPath(instanceID string) string {
	return hypervisor.LogPath(m.hypervisorSocketPath(instanceID))
}

// vsockPath returns the host end of an instance's vsock device.
func (m *Manager) vsockPath(instanceID string) string {
	return filepath.Join(m.runtimeDir(instanceID), vsockSocketFile)
}
