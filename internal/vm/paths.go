// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"path/filepath"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/hypervisor"
)

// DefaultRunDir is where runtime state lives unless configured otherwise.
const DefaultRunDir = "/run/dicer"

// An instance's files live in two places.
//
// The instance directory, owned by Definitions, is persistent. It holds what
// must survive a stop or a reboot: the overlay disk that is the guest's root
// filesystem, the serial console log that explains the last boot, and the
// snapshots, each a directory holding its own copy of the overlay disk.
//
// The runtime directory, under RunDir, is ephemeral and expected to be a
// tmpfs. It holds what only means something while a VMM is alive: the
// runtime state, the sockets, the config disk and the VMM's own log. It is
// keyed by ID rather than name, so a rename cannot orphan a running VM's.
//
// Every path is derived from the instance alone, which is what lets start,
// restore and recovery agree on them without recording any.
//
// Helpers returning a directory end in Dir; those returning a file, Path.
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
func (m *Manager) instanceDir(inst dicer.InstanceSpec) string {
	return m.definitions.InstanceDir(inst.Name)
}

// overlayDiskPath returns the writable disk holding an instance's root
// filesystem.
func (m *Manager) overlayDiskPath(inst dicer.InstanceSpec) string {
	return filepath.Join(m.instanceDir(inst), overlayDiskFile)
}

// serialLogPath returns the file an instance's serial console is written to.
func (m *Manager) serialLogPath(inst dicer.InstanceSpec) string {
	return filepath.Join(m.instanceDir(inst), serialLogFile)
}

// snapshotsDir returns the directory holding an instance's snapshots.
func (m *Manager) snapshotsDir(inst dicer.InstanceSpec) string {
	return filepath.Join(m.instanceDir(inst), snapshotsDirName)
}

// snapshotDir returns the directory holding one snapshot: its metadata, its
// copy of the overlay disk, and the hypervisor's own files, named as that
// hypervisor chooses.
func (m *Manager) snapshotDir(inst dicer.InstanceSpec, name string) string {
	return filepath.Join(m.snapshotsDir(inst), name)
}

// snapshotMetadataPath returns the file recording a snapshot's metadata.
func (m *Manager) snapshotMetadataPath(inst dicer.InstanceSpec, name string) string {
	return filepath.Join(m.snapshotDir(inst, name), snapshotMetadataFile)
}

// snapshotOverlayDiskPath returns a snapshot's copy of the overlay disk.
func (m *Manager) snapshotOverlayDiskPath(inst dicer.InstanceSpec, name string) string {
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
