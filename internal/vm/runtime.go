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

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/atomicfile"
	"github.com/dicer-sh/dicer/internal/process"
)

// Runtime state belongs to the lifecycle layer, not the definition store: it
// is written by the operations in this package and is meaningless without
// them.
//
// Unlike definitions it is not cached in memory. It is read and written
// straight to disk, because the daemon and the hypervisor processes outlive
// each other in different failure modes, and a stale cache would be worse
// than a syscall.

// Runtime returns an instance's runtime state. An instance with no runtime
// file is reported as Stopped rather than as an error -- that is the normal
// state of a defined-but-never-started instance, and of everything after a
// reboot.
func (m *Manager) Runtime(inst dicer.InstanceSpec) (dicer.InstanceStatus, error) {
	return m.readRuntime(inst.ID)
}

// readRuntime is dicer.InstanceStatus by instance ID, for the paths that have no more
// than that to hand.
func (m *Manager) readRuntime(instanceID string) (dicer.InstanceStatus, error) {
	data, err := os.ReadFile(m.runtimeStatePath(instanceID))
	if errors.Is(err, fs.ErrNotExist) {
		return dicer.InstanceStatus{InstanceID: instanceID, State: dicer.StateStopped}, nil
	}

	var rt dicer.InstanceStatus
	if err == nil {
		err = json.Unmarshal(data, &rt)
	}
	if err != nil {
		// A corrupt runtime file means we cannot know what is running.
		// Report Failed so recovery cleans up rather than quietly assuming
		// there is nothing to clean.
		m.logger.Warn("corrupt runtime state, treating instance as failed",
			"instance_id", instanceID, "error", err)
		return dicer.InstanceStatus{
			InstanceID: instanceID,
			State:      dicer.StateFailed,
			StateError: "corrupt runtime state",
		}, nil
	}

	return rt, nil
}

// writeRuntime records an instance's runtime state.
func (m *Manager) writeRuntime(rt dicer.InstanceStatus) error {
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
	held              dicer.Resources
	imageDigest       string

	// restarts is the restart count the instance carries into this run:
	// zero for a start a user asked for.
	restarts int

	// healthCheck is the check the run is monitored with, or nil.
	healthCheck *dicer.HealthCheck
}

// recordRunning records that an instance is running on vmm, holding what it
// was admitted with, and returns what it recorded. Everything else it
// records is derived from the instance, so start and restore record the same
// thing.
func (m *Manager) recordRunning(inst dicer.InstanceSpec, vmm *process.Process, run runRecord) (dicer.InstanceStatus, error) {
	pid := vmm.PID()

	rt := dicer.InstanceStatus{
		InstanceID:           inst.ID,
		State:                dicer.StateRunning,
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
		return dicer.InstanceStatus{}, fmt.Errorf("record runtime state: %w", err)
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

// prepareRuntimeDir readies an instance's runtime directory for a new VMM.
//
// The directory may be left over from one that died: a Failed instance keeps
// it so that the VMM's log survives to explain the failure. The sockets that
// VMM bound are kept with it, and a hypervisor refuses to bind over them, so
// they are removed here. Nothing can be listening on them -- the instance is
// not active, or it would not be starting.
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
