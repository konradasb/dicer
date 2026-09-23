// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/process"
)

// Recover reconciles recorded runtime state against what is actually running
// on the host, and takes charge of the VMMs that are. It runs once at daemon
// startup, before the API is served.
//
// No write-ahead log is needed. Because every host resource an instance
// holds is derived from its ID, we never need to know what a crashed
// start had got around to acquiring: we can simply ask for each instance
// whether its VMM is alive, and if it is not, release everything it could
// possibly have held. Releasing something that was never acquired is a no-op.
//
// For every instance not recorded as Stopped:
//
//   - Running or Paused, VMM alive: adopt it. From here on its exit is
//     noticed like that of a VMM this daemon started.
//   - Running or Paused, VMM gone: it ended while no daemon was watching.
//     Read how from what the guest reported, and apply the instance's
//     restart policy, as if the end had been seen as it happened.
//   - Starting or Stopping: the daemon died part way through. Kill the VMM if
//     there is one, release host resources and mark the instance Failed.
//   - Restarting: waiting for a restart the previous daemon scheduled. It is
//     scheduled again, for the same time.
//   - Failed: already cleaned up when it failed; left as it is.
//
// After a host reboot the runtime directory is empty, so every instance reads
// as Stopped and this pass does nothing -- which is correct, since a reboot
// already released every host resource.
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

	// The address manager is told which instances are live rather than looking it
	// up: address policy has no business reading the definition store.
	live := make(map[string]struct{}, len(instances))
	networks := make(map[string]struct{})
	for _, inst := range instances {
		live[inst.ID] = struct{}{}
		networks[inst.NetworkName] = struct{}{}
	}

	// Include networks that no longer have any instance, so their leftover
	// tables get emptied too.
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
func (m *Manager) recoverInstance(ctx context.Context, inst dicer.InstanceSpec) recovery {
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
	case dicer.StateStopped, dicer.StateFailed:
		return recoveryNone
	case dicer.StateRestarting:
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
		// Its VMM is gone: the guest ended while no daemon was watching.
		// Only the guest's own report can say how, since a VMM this daemon
		// did not start leaves no exit status behind.
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
	m.record(inst, dicer.ActionDied, "dicer.InstanceSpec failed: "+cause.Error(), nil)

	return recoveryCleaned
}

// operationOf names the operation an in-progress state belongs to.
func operationOf(s dicer.InstanceState) string {
	switch s {
	case dicer.StateStarting:
		return opStart
	case dicer.StateStopping:
		return opStop
	default:
		return string(s)
	}
}

// StartOnBoot starts every instance whose restart policy asks to be running
// whenever the daemon is: always, and unless-stopped if no user stopped it.
// It runs after Recover, and skips instances Recover adopted as running or
// scheduled to restart. A Failed instance is started too: its VMM most likely
// died while the daemon was down, and its policy wants it running.
func (m *Manager) StartOnBoot(ctx context.Context) {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		m.logger.WarnContext(ctx, "cannot list instances to start on boot", "error", err)
		return
	}

	for _, inst := range instances {
		if ctx.Err() != nil {
			// The daemon is shutting down; the next one starts the rest.
			return
		}
		if !inst.Restart.StartsOnBoot(inst.StoppedByUser) {
			continue
		}

		rt, err := m.Runtime(inst)
		if err != nil || (rt.State != dicer.StateStopped && rt.State != dicer.StateFailed) {
			continue
		}

		if err := m.Start(ctx, inst); err != nil {
			// One instance failing to start must not stop the others.
			m.logger.ErrorContext(ctx, "start on boot failed",
				"instance", inst.Name, "error", err)
			continue
		}
	}
}
