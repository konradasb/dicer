// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// Recover reconciles recorded instance status with what is running on the host.
// It runs once at startup, before the API is served. For each instance:
//
//   - Running or Paused, VMM alive: adopt it.
//   - Running or Paused, VMM gone: handle it as an unexpected end.
//   - Starting or Stopping: kill the VMM, release resources, mark Failed.
//   - Restarting: schedule the restart again.
//   - Stopped or Failed: nothing.
func (m *Manager) Recover(ctx context.Context) {
	instances := m.definitions.Instances()

	var adopted, cleaned int
	for _, instance := range instances {
		switch m.recoverInstance(ctx, instance) {
		case recoveryAdopted:
			adopted++
		case recoveryCleaned:
			cleaned++
		case recoveryNone:
		}
	}

	live := make(map[string]struct{}, len(instances))
	networks := make(map[string]struct{})
	for _, instance := range instances {
		live[instance.ID] = struct{}{}
		networks[instance.NetworkName] = struct{}{}
	}

	// Include networks with no instances so stale allocations are dropped.
	for _, n := range m.definitions.Networks() {
		networks[n.Name] = struct{}{}
	}

	released, err := m.networks.Reconcile(slices.Collect(maps.Keys(networks)), live)
	if err != nil {
		m.logger.WarnContext(ctx, "failed to reconcile network allocations", "error", err)
	}

	m.restoreAdoptedNetworks(ctx, instances)

	m.logger.InfoContext(ctx, "recovery complete",
		"instances", len(instances),
		"adopted", adopted,
		"cleaned_up", cleaned,
		"allocations_released", released,
	)
}

// recovery is what recoverInstance did with an instance.
type recovery int

const (
	recoveryNone recovery = iota
	recoveryAdopted
	recoveryCleaned
)

// recoverInstance applies Recover to one instance.
func (m *Manager) recoverInstance(ctx context.Context, instance types.InstanceSpec) recovery {
	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := m.Status(instance)
	if err != nil {
		m.logger.WarnContext(ctx, "cannot read instance status, skipping",
			"instance", instance.Name, "error", err)
		return recoveryNone
	}

	switch status.State {
	case types.InstanceStateStopped, types.InstanceStateFailed:
		return recoveryNone
	case types.InstanceStateRestarting:
		m.scheduleRestart(ctx, instance.ID, status.NextRestartAt)
		m.logger.InfoContext(ctx, "instance is waiting to restart",
			"instance", instance.Name, "restart_at", status.NextRestartAt)
		return recoveryNone
	}

	var vmm *process.Process
	if status.VMMPID != nil {
		vmm, err = m.attach(*status.VMMPID, status.HypervisorSocketPath)
		if err != nil {
			m.logger.DebugContext(ctx, "recorded hypervisor is not running",
				"instance", instance.Name, "pid", *status.VMMPID, "error", err)
		}
	}

	if vmm != nil && status.State.IsActive() {
		m.supervise(ctx, instance, vmm, status)
		m.logger.InfoContext(ctx, "adopted running instance",
			"instance", instance.Name, "state", status.State, "pid", vmm.PID())
		return recoveryAdopted
	}

	if status.State.IsActive() {
		m.logger.WarnContext(ctx, "instance ended while dicerd was not running",
			"instance", instance.Name, "recorded_state", status.State)
		exit := m.readExit(instance.ID, process.ErrExitStatusUnknown)
		if !exit.Clean() {
			exit.Failure = fmt.Errorf("%w, while dicerd was not running", exit.Failure)
		}
		m.ended(ctx, instance, status, exit)
		return recoveryCleaned
	}

	cause := fmt.Errorf("%s interrupted by a daemon restart", operationOf(status.State))

	if vmm != nil {
		vmm.Terminate()
	}

	m.logger.WarnContext(ctx, "instance did not survive daemon restart, cleaning up",
		"instance", instance.Name, "recorded_state", status.State, "cause", cause)

	m.teardownNetwork(ctx, instance)
	m.fail(instance.ID, cause)
	m.record(instance, events.ActionDied, "Instance failed: "+cause.Error(), nil)

	return recoveryCleaned
}

// restoreAdoptedNetworks sets up again the networks whose instances
// survived a daemon restart, and starts their DNS servers: their bridges
// are up, as they were left, but the host network has to know them, to set
// them up again when firewalld reloads, and their guests ask the gateway
// still.
func (m *Manager) restoreAdoptedNetworks(ctx context.Context, instances []types.InstanceSpec) {
	restored := make(map[string]bool)
	for _, instance := range instances {
		if restored[instance.NetworkName] {
			continue
		}
		status, err := m.Status(instance)
		if err != nil || !status.State.IsActive() {
			continue
		}
		nw, err := m.definitions.Network(instance.NetworkName)
		if err != nil {
			continue
		}
		restored[instance.NetworkName] = true
		m.restoreNetwork(ctx, nw)
	}
}

// restoreNetwork sets an adopted network up again, under its lock.
func (m *Manager) restoreNetwork(ctx context.Context, nw types.Network) {
	lock := m.networkLock(nw.Name)
	lock.Lock()
	defer lock.Unlock()

	if err := m.hostNetwork.SetupBridge(ctx, &nw); err != nil {
		m.logger.WarnContext(ctx, "cannot set up an adopted network's bridge again",
			"network", nw.Name, "error", err)
	}
	m.serveDNS(ctx, nw)
}

// operationOf names the operation an in-progress state belongs to.
func operationOf(s types.InstanceState) string {
	switch s {
	case types.InstanceStateStarting:
		return operationStart
	case types.InstanceStateStopping:
		return operationStop
	default:
		return string(s)
	}
}

// StartOnBoot starts every Stopped or Failed instance whose restart policy
// starts it on boot. It runs after Recover.
func (m *Manager) StartOnBoot(ctx context.Context) {
	instances := m.definitions.Instances()

	for _, instance := range instances {
		if ctx.Err() != nil {
			return
		}
		if !instance.Restart.StartsOnBoot(instance.StoppedByUser) {
			continue
		}

		status, err := m.Status(instance)
		if err != nil || (status.State != types.InstanceStateStopped && status.State != types.InstanceStateFailed) {
			continue
		}

		if err := m.Start(ctx, instance); err != nil {
			m.logger.ErrorContext(ctx, "start on boot failed",
				"instance", instance.Name, "error", err)
			continue
		}
	}
}
