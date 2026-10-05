// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// An instance's status is read from and written to its runtime directory
// directly, with no cache.

// Status returns an instance's status. An instance with no status file is
// Stopped.
func (m *Manager) Status(instance types.InstanceSpec) (types.InstanceStatus, error) {
	return m.readStatus(instance.ID)
}

// readStatus returns the status of an instance by ID.
func (m *Manager) readStatus(instanceID string) (types.InstanceStatus, error) {
	data, err := os.ReadFile(m.statusPath(instanceID))
	if errors.Is(err, fs.ErrNotExist) {
		return types.InstanceStatus{InstanceID: instanceID, State: types.InstanceStateStopped}, nil
	}

	var status types.InstanceStatus
	if err == nil {
		err = json.Unmarshal(data, &status)
	}
	if err != nil {
		// Report Failed so recovery cleans up.
		m.logger.Warn("corrupt instance status, treating instance as failed",
			"instance_id", instanceID, "error", err)
		return types.InstanceStatus{
			InstanceID: instanceID,
			State:      types.InstanceStateFailed,
			StateError: "corrupt instance status",
		}, nil
	}

	return status, nil
}

// writeStatus records an instance's status.
func (m *Manager) writeStatus(status types.InstanceStatus) error {
	if err := m.ensureRuntimeDir(status.InstanceID); err != nil {
		return err
	}

	status.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal instance status: %w", err)
	}

	return atomicfile.Write(m.statusPath(status.InstanceID), data, 0o600)
}

// runRecord is what a start or restore records of the guest it launched.
type runRecord struct {
	hypervisorVersion string
	held              types.Resources
	imageDigest       string

	restarts    int
	healthCheck *types.HealthCheck
}

// recordRunning records that an instance is running on vmm and returns the
// recorded state.
func (m *Manager) recordRunning(instance types.InstanceSpec, vmm *process.Process, run runRecord) (types.InstanceStatus, error) {
	pid := vmm.PID()

	status := types.InstanceStatus{
		InstanceID:           instance.ID,
		State:                types.InstanceStateRunning,
		VMMPID:               &pid,
		HypervisorSocketPath: m.hypervisorSocketPath(instance.ID),
		HypervisorVersion:    run.hypervisorVersion,
		VsockCID:             vsockCID(instance.ID),
		VsockPath:            m.vsockPath(instance.ID),
		VCPUs:                run.held.VCPUs,
		MemoryBytes:          run.held.MemoryBytes,
		ImageDigest:          run.imageDigest,
		HealthCheck:          run.healthCheck,
		StartedAt:            time.Now(),
		RestartCount:         run.restarts,
	}
	if err := m.writeStatus(status); err != nil {
		return types.InstanceStatus{}, fmt.Errorf("record instance status: %w", err)
	}

	return status, nil
}

// removeRuntimeDir removes an instance's runtime directory and everything
// in it, its status included.
func (m *Manager) removeRuntimeDir(instanceID string) error {
	dir := m.runtimeDir(instanceID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove runtime directory %s: %w", dir, err)
	}

	return nil
}

// ensureRuntimeDir creates an instance's runtime directory.
func (m *Manager) ensureRuntimeDir(instanceID string) error {
	dir := m.runtimeDir(instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime directory %s: %w", dir, err)
	}

	return nil
}

// prepareRuntimeDir readies an instance's runtime directory for a new VMM:
// it removes sockets a previous VMM left behind and links in the overlay
// disk and console log from the instance directory, which the VMM finds
// there by name. The directory itself is kept for the previous VMM's log.
func (m *Manager) prepareRuntimeDir(instance types.InstanceSpec) error {
	if err := m.ensureRuntimeDir(instance.ID); err != nil {
		return err
	}

	for _, path := range []string{m.hypervisorSocketPath(instance.ID), m.vsockPath(instance.ID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove stale socket %s: %w", path, err)
		}
	}

	// Linked afresh each time: a rename moves the instance directory.
	links := map[string]string{
		overlayDiskFile: m.overlayDiskPath(instance),
		serialLogFile:   m.serialLogPath(instance),
	}
	for name, target := range links {
		link := filepath.Join(m.runtimeDir(instance.ID), name)
		if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove stale link %s: %w", link, err)
		}
		if err := os.Symlink(target, link); err != nil {
			return fmt.Errorf("link %s into the runtime directory: %w", name, err)
		}
	}

	return nil
}
