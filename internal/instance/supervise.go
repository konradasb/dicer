// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package instance

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/health"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/process"
)

// The Manager holds a process handle for the VMM of every active instance and
// watches each for exit. An operation that stops a VMM deregisters its handle
// under the instance lock, so the watcher ignores that exit. Any other exit is
// unexpected: its cause is read from the guest (exit.go) and the restart
// policy decides what happens next (restart.go).

// supervised is an active instance's VMM and its health monitor.
type supervised struct {
	vmm *process.Process

	// health is the instance's health monitor, or nil if it has no check.
	// stopMonitor ends it.
	health      *health.Monitor
	stopMonitor context.CancelFunc
}

// supervise registers vmm as the VMM of instance, watches it for exit, and
// monitors health if status has a check. The caller must hold the instance
// lock.
func (m *Manager) supervise(ctx context.Context, instance Spec, vmm *process.Process, status Status) {
	ctx = context.WithoutCancel(ctx)

	s := &supervised{vmm: vmm, stopMonitor: func() {}}
	if status.HealthCheck != nil {
		var monitorCtx context.Context
		monitorCtx, s.stopMonitor = context.WithCancel(ctx)
		s.health = health.NewMonitor(*status.HealthCheck, status.StartedAt)
		m.watchers.Go(func() { m.monitor(monitorCtx, instance, vmm, status.VsockPath, s.health) })
	}

	m.vmmsMu.Lock()
	m.vmms[instance.ID] = s
	m.vmmsMu.Unlock()

	m.watchers.Go(func() {
		select {
		case <-vmm.Done():
			m.handleExit(ctx, instance, vmm)
		case <-m.closing:
		}
	})
}

// supervision returns what supervises an instance's VMM, or nil if none is
// registered.
func (m *Manager) supervision(instanceID string) *supervised {
	m.vmmsMu.Lock()
	defer m.vmmsMu.Unlock()
	return m.vmms[instanceID]
}

// vmm returns the registered VMM of an instance, or nil.
func (m *Manager) vmm(instanceID string) *process.Process {
	if s := m.supervision(instanceID); s != nil {
		return s.vmm
	}
	return nil
}

// forget deregisters an instance's VMM, marking its exit as expected, and
// ends its health monitor. The caller must hold the instance lock.
func (m *Manager) forget(instanceID string) {
	m.vmmsMu.Lock()
	s := m.vmms[instanceID]
	delete(m.vmms, instanceID)
	m.vmmsMu.Unlock()

	if s != nil {
		s.stopMonitor()
	}
}

// stopVMM ends an instance's VMM and waits for it to exit. A graceful stop
// first asks the guest to shut down within stopGracePeriod. Otherwise, or
// after that, the guest's disks are synced and the VMM is shut down, then
// killed if it overstays. The caller must hold the instance lock.
func (m *Manager) stopVMM(ctx context.Context, instance Spec, status Status, graceful bool) stopOutcome {
	vmm := m.vmm(instance.ID)
	if vmm == nil {
		return stopNotRunning
	}
	defer m.forget(instance.ID)

	outcome := stopForced
	if graceful {
		if outcome = m.shutdownGracefully(ctx, instance, status, vmm); outcome == stopGraceful {
			return outcome
		}
	}

	m.syncGuest(ctx, instance, status)

	if hypervisor, err := m.connect(instance, status); err == nil {
		m.logger.DebugContext(ctx, "shutting down hypervisor", "instance", instance.Name)
		_ = hypervisor.Shutdown(ctx)
	} else {
		m.logger.DebugContext(ctx, "hypervisor not reachable, killing it",
			"instance", instance.Name, "error", err)
		_ = vmm.Kill()
	}

	select {
	case <-vmm.Done():
	case <-time.After(m.shutdownTimeout):
		m.logger.WarnContext(ctx, "hypervisor did not exit in time, killing it",
			"instance", instance.Name, "pid", vmm.PID())
		vmm.Terminate()
	}
	return outcome
}

// stopOutcome is how stopVMM ended a VM.
type stopOutcome int

const (
	stopNotRunning stopOutcome = iota
	stopGraceful               // the guest shut down when asked
	stopTimedOut               // the guest did not shut down within the grace period
	stopForced                 // the guest was not asked to shut down
)

// stopMessage describes a stop for the events log.
func stopMessage(outcome stopOutcome, grace, took, ranFor time.Duration) string {
	if outcome == stopNotRunning {
		return "Instance was not running; nothing to stop"
	}

	message := "Stopped instance"
	if ranFor > 0 {
		message += " after running for " + humanize.Duration(ranFor)
	}
	switch outcome {
	case stopGraceful:
		return message + ": guest shut down gracefully in " + humanize.Duration(took)
	case stopTimedOut:
		return message + fmt.Sprintf(": guest did not shut down within the %s grace period; hypervisor shut down", humanize.Duration(grace))
	default:
		return message + ": guest could not be asked to shut down (paused, or its agent is too old); hypervisor shut down"
	}
}

// shutdownGracefully asks a running guest to shut down and waits up to
// stopGracePeriod for its VMM to exit.
func (m *Manager) shutdownGracefully(ctx context.Context, instance Spec, status Status, vmm *process.Process) stopOutcome {
	if status.State != StateRunning || status.VsockPath == "" {
		return stopForced
	}

	if err := m.shutdownGuest(ctx, status.VsockPath); err != nil {
		m.logger.DebugContext(ctx, "cannot ask the guest to shut down, ending it",
			"instance", instance.Name, "error", err)
		return stopForced
	}

	select {
	case <-vmm.Done():
		m.logger.DebugContext(ctx, "guest shut down", "instance", instance.Name)
		return stopGraceful
	case <-time.After(m.stopGracePeriod):
		m.logger.WarnContext(ctx, "guest did not shut down in time, ending it",
			"instance", instance.Name, "grace_period", m.stopGracePeriod)
		return stopTimedOut
	}
}

// handleExit handles a VMM that exited unexpectedly. The runtime directory
// is kept for the VMM's log.
func (m *Manager) handleExit(ctx context.Context, instance Spec, vmm *process.Process) {
	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	if m.vmm(instance.ID) != vmm {
		// Stopped, deleted or replaced while we waited for the lock.
		return
	}
	m.forget(instance.ID)

	// Apply the current definition's restart policy.
	if current, err := m.store.Instance(instance.ID); err == nil {
		instance = current
	}

	status, err := m.statusOf(instance)
	if err != nil {
		m.logger.WarnContext(ctx, "cannot read instance status", "instance", instance.Name, "error", err)
	}

	m.ended(ctx, instance, status, m.readExit(instance.ID, vmm.Err()))
}

// ended records an unexpected end, releases the instance's network and
// applies its restart policy: Stopped after a clean exit, Failed otherwise,
// or Restarting. prev is the status before it ended. The caller must
// hold the instance lock.
func (m *Manager) ended(ctx context.Context, instance Spec, prev Status, exit Exit) {
	m.teardownNetwork(ctx, instance)

	var ranFor time.Duration
	if !prev.StartedAt.IsZero() {
		ranFor = time.Since(prev.StartedAt)
	}
	decision := decideRestart(instance.Restart, exit, prev.RestartCount, ranFor)

	status := prev
	status.InstanceID = instance.ID
	forgetProcess(&status)
	status.ExitCode = exit.Code
	status.FinishedAt = time.Now()
	status.RestartCount = decision.restarts
	status.StateError = ""

	switch {
	case decision.restart:
		status.State = StateRestarting
		status.NextRestartAt = status.FinishedAt.Add(decision.delay)
		if !exit.Clean() {
			status.StateError = exit.Failure.Error()
		}
	case exit.Clean():
		status.State = StateStopped
	case decision.gaveUp:
		status.State = StateFailed
		status.StateError = fmt.Sprintf("gave up after %s: %v", humanize.Count(decision.restarts, "restart"), exit.Failure)
	default:
		status.State = StateFailed
		status.StateError = exit.Failure.Error()
	}

	if err := m.writeStatus(status); err != nil {
		m.logger.WarnContext(ctx, "cannot record how the instance ended", "instance", instance.Name, "error", err)
	}
	m.recordEnd(instance, exit, decision, ranFor)

	attrs := []any{"instance", instance.Name, "state", status.State, "restart_policy", instance.Restart.String()}
	if exit.Code != nil {
		attrs = append(attrs, "exit_code", *exit.Code)
	}
	if !exit.Clean() {
		attrs = append(attrs, "error", exit.Failure)
	}

	if decision.restart {
		m.logger.WarnContext(ctx, "instance ended, restarting it", append(attrs,
			"restart_count", status.RestartCount, "delay", decision.delay)...)
		m.scheduleRestart(ctx, instance.ID, status.NextRestartAt)

		return
	}

	m.notifyWaiters(status)
	m.scheduleRemoval(ctx, instance)

	if exit.Clean() {
		m.logger.InfoContext(ctx, "instance ended", attrs...)

		return
	}
	m.logger.ErrorContext(ctx, "instance ended unexpectedly", attrs...)
}

// scheduleRemoval deletes an instance with RemoveOnExit set. It runs in a
// goroutine because callers hold the instance lock, which Delete takes.
func (m *Manager) scheduleRemoval(ctx context.Context, instance Spec) {
	if !instance.RemoveOnExit {
		return
	}

	ctx = context.WithoutCancel(ctx)

	m.watchers.Go(func() {
		if err := m.delete(ctx, instance, false); err != nil {
			m.logger.ErrorContext(ctx, "cannot delete the instance that asked to be deleted when it stopped",
				"instance", instance.Name, "error", err)
		}
	})
}

// pendingRestart is a scheduled restart. It is compared by identity, so a
// timer for a cancelled or replaced restart does nothing.
type pendingRestart struct {
	timer *time.Timer
}

// scheduleRestart starts a Restarting instance again at the given time. A
// closing manager schedules nothing; the next daemon picks it up.
func (m *Manager) scheduleRestart(ctx context.Context, instanceID string, at time.Time) {
	m.restartsMu.Lock()
	defer m.restartsMu.Unlock()

	if m.isClosing() {
		return
	}
	if pending := m.restarts[instanceID]; pending != nil {
		pending.timer.Stop()
	}

	ctx = context.WithoutCancel(ctx)

	pending := &pendingRestart{}
	pending.timer = time.AfterFunc(m.restartWait(at), func() { m.restart(ctx, instanceID, pending) })
	m.restarts[instanceID] = pending
}

// cancelRestart drops an instance's pending restart, if any. The caller must
// hold the instance lock.
func (m *Manager) cancelRestart(instanceID string) {
	m.restartsMu.Lock()
	defer m.restartsMu.Unlock()

	if pending := m.restarts[instanceID]; pending != nil {
		pending.timer.Stop()
		delete(m.restarts, instanceID)
	}
}

// restart starts an instance again for its restart policy. A failed restart
// is handled as another end.
func (m *Manager) restart(ctx context.Context, instanceID string, pending *pendingRestart) {
	m.restartsMu.Lock()
	if m.isClosing() || m.restarts[instanceID] != pending {
		m.restartsMu.Unlock()
		return
	}
	delete(m.restarts, instanceID)
	m.restarting.Add(1)
	m.restartsMu.Unlock()
	defer m.restarting.Done()

	lock := m.lock(instanceID)
	lock.Lock()
	defer lock.Unlock()

	// Deleted, or edited, while the restart waited.
	instance, err := m.store.Instance(instanceID)
	if err != nil {
		return
	}
	status, err := m.statusOf(instance)
	if err != nil || status.State != StateRestarting {
		return
	}

	m.metrics.restarts.Inc()
	m.logger.InfoContext(ctx, "restarting instance", "instance", instance.Name, "restart_count", status.RestartCount)

	if err := m.admit(instance, instance.Resources()); err != nil {
		m.ended(ctx, instance, status, Exit{Failure: fmt.Errorf("restart: %w", err)})
		return
	}
	// The boot is watched, but not waited for: a guest that does not boot
	// ends again, as another failure.
	if _, err := m.boot(ctx, instance, status.RestartCount); err != nil {
		m.ended(ctx, instance, status, Exit{Failure: fmt.Errorf("restart: %w", err)})
	}
}

// isClosing reports whether Close has been called.
func (m *Manager) isClosing() bool {
	select {
	case <-m.closing:
		return true
	default:
		return false
	}
}

// Close stops watching VMMs and restarting instances, and waits for work under
// way. The VMMs keep running for the next daemon to adopt.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.restartsMu.Lock()
		close(m.closing)
		for _, pending := range m.restarts {
			pending.timer.Stop()
		}
		clear(m.restarts)
		m.restartsMu.Unlock()
	})

	// A restart may add a watcher, so wait for restarts first.
	m.restarting.Wait()
	m.closeWakers()
	m.watchers.Wait()
}
