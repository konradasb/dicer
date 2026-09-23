// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// Recover reconciles recorded runtime state with what is running on the host.
// It runs once at startup, before the API is served. For each instance:
//
//   - Running or Paused, VMM alive: adopt it.
//   - Running or Paused, VMM gone: handle it as an unexpected end.
//   - Starting or Stopping: kill the VMM, release resources, mark Failed.
//   - Restarting: schedule the restart again.
//   - Stopped or Failed: nothing.
func (m *Manager) Recover(ctx context.Context) error {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		return err
	}

	var adopted, cleaned int
	for _, inst := range instances {
		switch m.recoverInstance(ctx, inst) {
		case recoveryAdopted:
			adopted++
		case recoveryCleaned:
			cleaned++
		case recoveryNone:
		}
	}

	live := make(map[string]struct{}, len(instances))
	networks := make(map[string]struct{})
	for _, inst := range instances {
		live[inst.ID] = struct{}{}
		networks[inst.NetworkName] = struct{}{}
	}

	// Include networks with no instances so stale allocations are dropped.
	if all, err := m.definitions.ListNetworks(); err == nil {
		for _, n := range all {
			networks[n.Name] = struct{}{}
		}
	}

	released, err := m.addresses.Reconcile(slices.Collect(maps.Keys(networks)), live)
	if err != nil {
		m.logger.WarnContext(ctx, "failed to reconcile address allocations", "error", err)
	}

	m.logger.InfoContext(ctx, "recovery complete",
		"instances", len(instances),
		"adopted", adopted,
		"cleaned_up", cleaned,
		"allocations_released", released,
	)

	return nil
}

type recovery int

const (
	recoveryNone recovery = iota
	recoveryAdopted
	recoveryCleaned
)

// recoverInstance applies Recover to one instance.
func (m *Manager) recoverInstance(ctx context.Context, inst types.InstanceSpec) recovery {
	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	rt, err := m.Runtime(inst)
	if err != nil {
		m.logger.WarnContext(ctx, "cannot read runtime state, skipping",
			"instance", inst.Name, "error", err)
		return recoveryNone
	}

	switch rt.State {
	case types.StateStopped, types.StateFailed:
		return recoveryNone
	case types.StateRestarting:
		m.scheduleRestart(ctx, inst.ID, rt.NextRestartAt)
		m.logger.InfoContext(ctx, "instance is waiting to restart",
			"instance", inst.Name, "restart_at", rt.NextRestartAt)
		return recoveryNone
	}

	var vmm *process.Process
	if rt.HypervisorPID != nil {
		vmm, err = m.attach(*rt.HypervisorPID, rt.HypervisorSocketPath)
		if err != nil {
			m.logger.DebugContext(ctx, "recorded hypervisor is not running",
				"instance", inst.Name, "pid", *rt.HypervisorPID, "error", err)
		}
	}

	if vmm != nil && rt.State.IsActive() {
		m.supervise(ctx, inst, vmm, rt)
		m.logger.InfoContext(ctx, "adopted running instance",
			"instance", inst.Name, "state", rt.State, "pid", vmm.PID())
		return recoveryAdopted
	}

	if rt.State.IsActive() {
		m.logger.WarnContext(ctx, "instance ended while dicerd was not running",
			"instance", inst.Name, "recorded_state", rt.State)
		exit := m.readExit(inst.ID, process.ErrExitStatusUnknown)
		if !exit.Clean() {
			exit.Failure = fmt.Errorf("%w, while dicerd was not running", exit.Failure)
		}
		m.ended(ctx, inst, rt, exit)
		return recoveryCleaned
	}

	cause := fmt.Errorf("%s interrupted by a daemon restart", operationOf(rt.State))

	if vmm != nil {
		vmm.Terminate()
	}

	m.logger.WarnContext(ctx, "instance did not survive daemon restart, cleaning up",
		"instance", inst.Name, "recorded_state", rt.State, "cause", cause)

	m.teardownNetwork(ctx, inst)
	m.fail(inst.ID, cause)
	m.record(inst, types.ActionDied, "Instance failed: "+cause.Error(), nil)

	return recoveryCleaned
}

// operationOf names the operation an in-progress state belongs to.
func operationOf(s types.InstanceState) string {
	switch s {
	case types.StateStarting:
		return opStart
	case types.StateStopping:
		return opStop
	default:
		return string(s)
	}
}

// StartOnBoot starts every Stopped or Failed instance whose restart policy
// starts it on boot. It runs after Recover.
func (m *Manager) StartOnBoot(ctx context.Context) {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		m.logger.WarnContext(ctx, "cannot list instances to start on boot", "error", err)
		return
	}

	for _, inst := range instances {
		if ctx.Err() != nil {
			return
		}
		if !inst.Restart.StartsOnBoot(inst.StoppedByUser) {
			continue
		}

		rt, err := m.Runtime(inst)
		if err != nil || (rt.State != types.StateStopped && rt.State != types.StateFailed) {
			continue
		}

		if err := m.Start(ctx, inst); err != nil {
			m.logger.ErrorContext(ctx, "start on boot failed",
				"instance", inst.Name, "error", err)
			continue
		}
	}
}
