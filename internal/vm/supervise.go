// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// The Manager holds a process handle for the VMM of every active instance and
// watches each for exit. An operation that stops a VMM deregisters its handle
// under the instance lock, so the watcher ignores that exit. Any other exit is
// unexpected: its cause is read from the guest (exit.go) and the restart
// policy decides what happens next (restart.go).

// supervised is an active instance's VMM and its health monitor.
type supervised struct {
	proc *process.Process

	// health is the instance's health monitor, or nil if it has no check.
	// stopMonitor ends it.
	health      *healthMonitor
	stopMonitor context.CancelFunc
}

// supervise registers p as the VMM of inst, watches it for exit, and monitors
// health if rt has a check. The caller must hold the instance lock.
func (m *Manager) supervise(ctx context.Context, inst types.InstanceSpec, p *process.Process, rt types.InstanceStatus) {
	ctx = context.WithoutCancel(ctx)

	s := &supervised{proc: p, stopMonitor: func() {}}
	if rt.HealthCheck != nil {
		var monitorCtx context.Context
		monitorCtx, s.stopMonitor = context.WithCancel(ctx)
		s.health = newHealthMonitor(*rt.HealthCheck, rt.StartedAt)
		m.watchers.Go(func() { m.monitor(monitorCtx, inst, p, rt.VsockPath, s.health) })
	}

	m.vmmsMu.Lock()
	m.vmms[inst.ID] = s
	m.vmmsMu.Unlock()

	m.watchers.Go(func() {
		select {
		case <-p.Done():
			m.handleExit(ctx, inst, p)
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
		return s.proc
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
func (m *Manager) stopVMM(ctx context.Context, inst types.InstanceSpec, rt types.InstanceStatus, graceful bool) stopOutcome {
	p := m.vmm(inst.ID)
	if p == nil {
		return stopNotRunning
	}
	defer m.forget(inst.ID)

	outcome := stopForced
	if graceful {
		if outcome = m.shutdownGracefully(ctx, inst, rt, p); outcome == stopGraceful {
			return outcome
		}
	}

	m.syncGuest(ctx, inst, rt)

	if hv, err := m.connect(inst, rt); err == nil {
		m.logger.DebugContext(ctx, "shutting down hypervisor", "instance", inst.Name)
		_ = hv.Shutdown(ctx)
	} else {
		m.logger.DebugContext(ctx, "hypervisor not reachable, killing it",
			"instance", inst.Name, "error", err)
		_ = p.Kill()
	}

	select {
	case <-p.Done():
	case <-time.After(m.shutdownTimeout):
		m.logger.WarnContext(ctx, "hypervisor did not exit in time, killing it",
			"instance", inst.Name, "pid", p.PID())
		p.Terminate()
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
		message += " after running for " + duration(ranFor)
	}
	switch outcome {
	case stopGraceful:
		return message + ": guest shut down gracefully in " + duration(took)
	case stopTimedOut:
		return message + fmt.Sprintf(": guest did not shut down within the %s grace period; hypervisor shut down", duration(grace))
	default:
		return message + ": guest could not be asked to shut down (paused, or its agent is too old); hypervisor shut down"
	}
}

// shutdownGracefully asks a running guest to shut down and waits up to
// stopGracePeriod for its VMM to exit.
func (m *Manager) shutdownGracefully(ctx context.Context, inst types.InstanceSpec, rt types.InstanceStatus, p *process.Process) stopOutcome {
	if rt.State != types.StateRunning || rt.VsockPath == "" {
		return stopForced
	}

	if err := m.shutdownGuest(ctx, rt.VsockPath); err != nil {
		m.logger.DebugContext(ctx, "cannot ask the guest to shut down, ending it",
			"instance", inst.Name, "error", err)
		return stopForced
	}

	select {
	case <-p.Done():
		m.logger.DebugContext(ctx, "guest shut down", "instance", inst.Name)
		return stopGraceful
	case <-time.After(m.stopGracePeriod):
		m.logger.WarnContext(ctx, "guest did not shut down in time, ending it",
			"instance", inst.Name, "grace_period", m.stopGracePeriod)
		return stopTimedOut
	}
}

// handleExit handles a VMM that exited unexpectedly. The runtime directory
// is kept for the VMM's log.
func (m *Manager) handleExit(ctx context.Context, inst types.InstanceSpec, p *process.Process) {
	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	if m.vmm(inst.ID) != p {
		// Stopped, deleted or replaced while we waited for the lock.
		return
	}
	m.forget(inst.ID)

	// Apply the current definition's restart policy.
	if current, err := m.definitions.GetInstance(inst.ID); err == nil {
		inst = current
	}

	rt, err := m.Runtime(inst)
	if err != nil {
		m.logger.WarnContext(ctx, "cannot read runtime state", "instance", inst.Name, "error", err)
	}

	m.ended(ctx, inst, rt, m.readExit(inst.ID, p.Err()))
}

// ended records an unexpected end, releases the instance's network and
// applies its restart policy: Stopped after a clean exit, Failed otherwise,
// or Restarting. prev is the runtime state before it ended. The caller must
// hold the instance lock.
func (m *Manager) ended(ctx context.Context, inst types.InstanceSpec, prev types.InstanceStatus, exit Exit) {
	m.teardownNetwork(ctx, inst)

	var ranFor time.Duration
	if !prev.StartedAt.IsZero() {
		ranFor = time.Since(prev.StartedAt)
	}
	d := decide(inst.Restart, exit, prev.RestartCount, ranFor)

	rt := prev
	rt.InstanceID = inst.ID
	forgetProcess(&rt)
	rt.ExitCode = exit.Code
	rt.FinishedAt = time.Now()
	rt.RestartCount = d.restarts
	rt.StateError = ""

	switch {
	case d.restart:
		rt.State = types.StateRestarting
		rt.NextRestartAt = rt.FinishedAt.Add(d.delay)
		if !exit.Clean() {
			rt.StateError = exit.Failure.Error()
		}
	case exit.Clean():
		rt.State = types.StateStopped
	case d.gaveUp:
		rt.State = types.StateFailed
		rt.StateError = fmt.Sprintf("gave up after %s: %v", plural(d.restarts, "restart"), exit.Failure)
	default:
		rt.State = types.StateFailed
		rt.StateError = exit.Failure.Error()
	}

	if err := m.writeRuntime(rt); err != nil {
		m.logger.WarnContext(ctx, "cannot record how the instance ended", "instance", inst.Name, "error", err)
	}
	m.recordEnd(inst, exit, d, ranFor)

	attrs := []any{"instance", inst.Name, "state", rt.State, "restart_policy", inst.Restart.String()}
	if exit.Code != nil {
		attrs = append(attrs, "exit_code", *exit.Code)
	}
	if !exit.Clean() {
		attrs = append(attrs, "error", exit.Failure)
	}

	if d.restart {
		m.logger.WarnContext(ctx, "instance ended, restarting it", append(attrs,
			"restart_count", rt.RestartCount, "delay", d.delay)...)
		m.scheduleRestart(ctx, inst.ID, rt.NextRestartAt)

		return
	}

	m.scheduleRemoval(ctx, inst)

	if exit.Clean() {
		m.logger.InfoContext(ctx, "instance ended", attrs...)

		return
	}
	m.logger.ErrorContext(ctx, "instance ended unexpectedly", attrs...)
}

// scheduleRemoval deletes an instance with RemoveOnExit set. It runs in a
// goroutine because callers hold the instance lock, which Delete takes.
func (m *Manager) scheduleRemoval(ctx context.Context, inst types.InstanceSpec) {
	if !inst.RemoveOnExit {
		return
	}

	ctx = context.WithoutCancel(ctx)

	m.watchers.Go(func() {
		if err := m.Delete(ctx, inst, false); err != nil {
			m.logger.ErrorContext(ctx, "cannot delete the instance that asked to be deleted when it stopped",
				"instance", inst.Name, "error", err)
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
	if p := m.restarts[instanceID]; p != nil {
		p.timer.Stop()
	}

	ctx = context.WithoutCancel(ctx)

	p := &pendingRestart{}
	p.timer = time.AfterFunc(m.restartWait(at), func() { m.restart(ctx, instanceID, p) })
	m.restarts[instanceID] = p
}

// cancelRestart drops an instance's pending restart, if any. The caller must
// hold the instance lock.
func (m *Manager) cancelRestart(instanceID string) {
	m.restartsMu.Lock()
	defer m.restartsMu.Unlock()

	if p := m.restarts[instanceID]; p != nil {
		p.timer.Stop()
		delete(m.restarts, instanceID)
	}
}

// restart starts an instance again for its restart policy. A failed restart
// is handled as another end.
func (m *Manager) restart(ctx context.Context, instanceID string, p *pendingRestart) {
	m.restartsMu.Lock()
	if m.isClosing() || m.restarts[instanceID] != p {
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
	inst, err := m.definitions.GetInstance(instanceID)
	if err != nil {
		return
	}
	rt, err := m.Runtime(inst)
	if err != nil || rt.State != types.StateRestarting {
		return
	}

	m.metrics.RecordInstanceRestart()
	m.logger.InfoContext(ctx, "restarting instance", "instance", inst.Name, "restart_count", rt.RestartCount)

	if err := m.admit(inst, inst.Resources()); err != nil {
		m.ended(ctx, inst, rt, failedExit(fmt.Errorf("restart: %w", err)))
		return
	}
	if err := m.boot(ctx, inst, rt.RestartCount); err != nil {
		m.ended(ctx, inst, rt, failedExit(fmt.Errorf("restart: %w", err)))
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
		for _, p := range m.restarts {
			p.timer.Stop()
		}
		clear(m.restarts)
		m.restartsMu.Unlock()
	})

	// A restart may add a watcher, so wait for restarts first.
	m.restarting.Wait()
	m.watchers.Wait()
}
