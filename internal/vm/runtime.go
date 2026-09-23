// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// Runtime state is read from and written to disk directly, with no cache.

// Runtime returns an instance's runtime state. An instance with no runtime
// file is Stopped.
func (m *Manager) Runtime(inst types.InstanceSpec) (types.InstanceStatus, error) {
	return m.readRuntime(inst.ID)
}

// readRuntime returns the runtime state of an instance by ID.
func (m *Manager) readRuntime(instanceID string) (types.InstanceStatus, error) {
	data, err := os.ReadFile(m.runtimeStatePath(instanceID))
	if errors.Is(err, fs.ErrNotExist) {
		return types.InstanceStatus{InstanceID: instanceID, State: types.StateStopped}, nil
	}

	var rt types.InstanceStatus
	if err == nil {
		err = json.Unmarshal(data, &rt)
	}
	if err != nil {
		// Report Failed so recovery cleans up.
		m.logger.Warn("corrupt runtime state, treating instance as failed",
			"instance_id", instanceID, "error", err)
		return types.InstanceStatus{
			InstanceID: instanceID,
			State:      types.StateFailed,
			StateError: "corrupt runtime state",
		}, nil
	}

	return rt, nil
}

// writeRuntime records an instance's runtime state.
func (m *Manager) writeRuntime(rt types.InstanceStatus) error {
	if err := m.ensureRuntimeDir(rt.InstanceID); err != nil {
		return err
	}

	rt.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime state: %w", err)
	}

	return atomicfile.Write(m.runtimeStatePath(rt.InstanceID), data, 0o600)
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
func (m *Manager) recordRunning(inst types.InstanceSpec, vmm *process.Process, run runRecord) (types.InstanceStatus, error) {
	pid := vmm.PID()

	rt := types.InstanceStatus{
		InstanceID:           inst.ID,
		State:                types.StateRunning,
		HypervisorPID:        &pid,
		HypervisorSocketPath: m.hypervisorSocketPath(inst.ID),
		HypervisorVersion:    run.hypervisorVersion,
		VsockCID:             vsockCID(inst.ID),
		VsockPath:            m.vsockPath(inst.ID),
		VCPUs:                run.held.VCPUs,
		MemoryBytes:          run.held.MemoryBytes,
		ImageDigest:          run.imageDigest,
		HealthCheck:          run.healthCheck,
		StartedAt:            time.Now(),
		RestartCount:         run.restarts,
	}
	if err := m.writeRuntime(rt); err != nil {
		return types.InstanceStatus{}, fmt.Errorf("record runtime state: %w", err)
	}

	return rt, nil
}

// clearRuntime removes an instance's runtime directory and everything in it.
func (m *Manager) clearRuntime(instanceID string) error {
	dir := m.runtimeDir(instanceID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove runtime dir %s: %w", dir, err)
	}

	return nil
}

// ensureRuntimeDir creates an instance's runtime directory.
func (m *Manager) ensureRuntimeDir(instanceID string) error {
	dir := m.runtimeDir(instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime dir %s: %w", dir, err)
	}

	return nil
}

// prepareRuntimeDir readies an instance's runtime directory for a new VMM,
// removing sockets a previous VMM left behind. The directory itself is kept
// for the previous VMM's log.
func (m *Manager) prepareRuntimeDir(instanceID string) error {
	if err := m.ensureRuntimeDir(instanceID); err != nil {
		return err
	}

	for _, path := range []string{m.hypervisorSocketPath(instanceID), m.vsockPath(instanceID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove stale socket %s: %w", path, err)
		}
	}

	return nil
}
