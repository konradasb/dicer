// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/process"
)

// Supervision is how the daemon knows what is running, as opposed to what it
// last recorded.
//
// The Manager holds a process handle for the VMM of every active instance --
// one it started, or one it adopted in Recover -- and watches each for exit.
// dicer.InstanceStatus state on disk is how that ownership survives a daemon restart; it
// is not how liveness is decided.
//
// An exit is expected when a lifecycle operation asked for it. Such an
// operation holds the instance lock while the VMM goes, and deregisters the
// handle before releasing the lock, so by the time the watcher gets the lock
// the handle it holds is no longer the registered one and it has nothing to
// do. Any other exit finds its handle still registered: the guest ended on
// its own -- its workload exited, it reset -- or the VMM died, to a crash, the
// OOM killer or an operator's kill -9. How it ended is read from what the
// guest reported (see exit.go), and the instance's restart policy decides
// what happens next (see restart.go): it may be started again, after a
// backoff, by a timer the Manager keeps until then.

// supervised is the daemon's hold on the VMM of an active instance: the
// process, and the health monitor that runs for as long as it does.
type supervised struct {
	proc *process.Process

	// health is the instance's health monitor, or nil if it has no check.
	// stopMonitor ends it.
	health      *healthMonitor
	stopMonitor context.CancelFunc
}

// supervise registers p as the VMM of inst, which runtime state rt records
// as running on it, watches it for exit, and monitors the instance's health
// if rt says how. The caller must hold the instance lock.
//
// The watchers outlive the request that started the VMM, so they keep ctx's
// values but not its cancellation.
func (m *Manager) supervise(ctx context.Context, inst dicer.InstanceSpec, p *process.Process, rt dicer.InstanceStatus) {
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

// stopVMM ends an instance's VMM and waits for it to exit. The exit is
// expected, so the handle is deregistered and the watcher leaves the
// instance alone. A VMM that is not registered is not running, and there is
// nothing to stop.
//
// A graceful stop first asks the guest to shut itself down, as docker stop
// asks a container: the workload is sent SIGTERM, and given
// stopGracePeriod to finish what it is doing and exit, which ends the VM.
// Whatever is still running after that, or on a stop that is not graceful,
// has its disks flushed and its VMM shut down, and is killed if it
// overstays.
//
// The caller must hold the instance lock.
func (m *Manager) stopVMM(ctx context.Context, inst dicer.InstanceSpec, rt dicer.InstanceStatus, graceful bool) stopOutcome {
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
	// stopNotRunning is a VM that was not running: there was nothing to end.
	stopNotRunning stopOutcome = iota
	// stopGraceful is a guest that shut itself down when asked.
	stopGraceful
	// stopTimedOut is a guest asked to shut down that did not within the
	// grace period, and was ended.
	stopTimedOut
	// stopForced is a guest ended without being asked to shut down: it
	// could not be asked, or was not to be waited for.
	stopForced
)

// stopMessage describes a stop that ended as outcome did, took took, and
// ended a run that had lasted ranFor.
func stopMessage(outcome stopOutcome, grace, took, ranFor time.Duration) string {
	if outcome == stopNotRunning {
		return "dicer.InstanceSpec was not running; nothing to stop"
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

// shutdownGracefully asks a running guest to shut itself down and waits,
// for up to stopGracePeriod, for its VMM p to exit. It reports whether it
// did. A guest that cannot be asked -- paused, or with an agent from before
// graceful stops -- is not waited for.
func (m *Manager) shutdownGracefully(ctx context.Context, inst dicer.InstanceSpec, rt dicer.InstanceStatus, p *process.Process) stopOutcome {
	if rt.State != dicer.StateRunning || rt.VsockPath == "" {
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

// handleExit handles a VMM that exited without being asked to: the guest
// ended, and the instance's restart policy decides what happens next.
//
// The runtime directory is kept: the VMM's log in it is the likeliest
// explanation of what happened.
func (m *Manager) handleExit(ctx context.Context, inst dicer.InstanceSpec, p *process.Process) {
	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	if m.vmm(inst.ID) != p {
		// Stopped, deleted or replaced while we waited for the lock.
		return
	}
	m.forget(inst.ID)

	// The definition may have changed since the VMM started -- its restart
	// policy included -- and it is the current one that applies.
	if current, err := m.definitions.GetInstance(inst.ID); err == nil {
		inst = current
	}

	rt, err := m.Runtime(inst)
	if err != nil {
		m.logger.WarnContext(ctx, "cannot read runtime state", "instance", inst.Name, "error", err)
	}

	m.ended(ctx, inst, rt, m.readExit(inst.ID, p.Err()))
}

// ended records that an instance's guest ended with exit, without being
// asked to, and applies its restart policy: the instance is left Stopped
// after a clean end, Failed after any other, or Restarting if the policy
// starts it again. prev is its runtime state from before it ended, which
// carries how long it ran and how many restarts it has had in a row.
//
// Whatever held the VMM is gone by now; the host resources it held are
// released here. The caller must hold the instance lock.
func (m *Manager) ended(ctx context.Context, inst dicer.InstanceSpec, prev dicer.InstanceStatus, exit Exit) {
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
		rt.State = dicer.StateRestarting
		rt.NextRestartAt = rt.FinishedAt.Add(d.delay)
		if !exit.Clean() {
			rt.StateError = exit.Failure.Error()
		}
	case exit.Clean():
		rt.State = dicer.StateStopped
	case d.gaveUp:
		rt.State = dicer.StateFailed
		rt.StateError = fmt.Sprintf("gave up after %s: %v", plural(d.restarts, "restart"), exit.Failure)
	default:
		rt.State = dicer.StateFailed
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

	// Nothing will start it again, so an instance asked to go when it stops
	// goes now.
	m.scheduleRemoval(ctx, inst)

	if exit.Clean() {
		m.logger.InfoContext(ctx, "instance ended", attrs...)

		return
	}
	m.logger.ErrorContext(ctx, "instance ended unexpectedly", attrs...)
}

// scheduleRemoval deletes an instance that asked to be deleted once it
// stopped, and does nothing for one that did not.
//
// It is the daemon's own doing rather than the client's, so it happens even
// if whoever started the instance has gone. The delete runs in a goroutine
// of its own because every caller holds the instance lock, which Delete
// takes for itself.
func (m *Manager) scheduleRemoval(ctx context.Context, inst dicer.InstanceSpec) {
	if !inst.RemoveOnExit {
		return
	}

	// The removal outlives whatever stopped the instance, so it keeps ctx's
	// values but not its cancellation.
	ctx = context.WithoutCancel(ctx)

	m.watchers.Go(func() {
		if err := m.Delete(ctx, inst, false); err != nil {
			m.logger.ErrorContext(ctx, "cannot delete the instance that asked to be deleted when it stopped",
				"instance", inst.Name, "error", err)
		}
	})
}

// pendingRestart is a restart waiting for its time. It is compared by
// identity: a timer that fires for a restart that was cancelled or replaced
// finds another in its place, or none, and does nothing.
type pendingRestart struct {
	timer *time.Timer
}

// scheduleRestart starts a Restarting instance again at the given time, or
// at once if it has passed. A manager that is closing schedules nothing: the
// instance stays Restarting on disk, and the next daemon picks it up.
func (m *Manager) scheduleRestart(ctx context.Context, instanceID string, at time.Time) {
	m.restartsMu.Lock()
	defer m.restartsMu.Unlock()

	if m.isClosing() {
		return
	}
	if p := m.restarts[instanceID]; p != nil {
		p.timer.Stop()
	}

	// The restart outlives whatever ended the instance, so it keeps ctx's
	// values but not its cancellation.
	ctx = context.WithoutCancel(ctx)

	p := &pendingRestart{}
	p.timer = time.AfterFunc(m.restartWait(at), func() { m.restart(ctx, instanceID, p) })
	m.restarts[instanceID] = p
}

// cancelRestart drops an instance's pending restart, if it has one: a user
// who stops, starts or deletes a Restarting instance has taken it over. The
// caller must hold the instance lock.
func (m *Manager) cancelRestart(instanceID string) {
	m.restartsMu.Lock()
	defer m.restartsMu.Unlock()

	if p := m.restarts[instanceID]; p != nil {
		p.timer.Stop()
		delete(m.restarts, instanceID)
	}
}

// restart starts an instance again for its restart policy. A restart that
// fails -- the host has no room for it, its image is gone -- is an end like
// any other, and the policy decides again.
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
	if err != nil || rt.State != dicer.StateRestarting {
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

// Close stops watching VMMs and restarting instances, and waits for the
// watchers and any restart under way to return. The VMMs themselves keep
// running: they outlive the daemon by design, and the next one adopts them.
// An instance waiting to restart stays Restarting, for the next one to
// restart.
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

	// A restart under way may still supervise the VMM it starts, which adds
	// a watcher: it has to finish before the watchers are waited for.
	m.restarting.Wait()
	m.watchers.Wait()
}
