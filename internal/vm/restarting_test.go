// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// A workload that exits 0 is a clean end: the instance is Stopped, not
// Failed, and says how it ended.
func TestCleanExitStopsInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.exit(t, 0)
	status := h.waitForState(t, types.InstanceStateStopped)

	if status.ExitCode == nil || *status.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", status.ExitCode)
	}
	if status.StateError != "" || status.FinishedAt.IsZero() {
		t.Errorf("status = %+v, want no error and a finish time", status)
	}
	if len(h.hostNetwork.removedTAPs) != 1 {
		t.Errorf("removed TAPs = %v, want the instance's network released", h.hostNetwork.removedTAPs)
	}
}

func TestNonZeroExitFailsInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.exit(t, 3)
	status := h.waitForState(t, types.InstanceStateFailed)

	if status.ExitCode == nil || *status.ExitCode != 3 {
		t.Errorf("exit code = %v, want 3", status.ExitCode)
	}
	if !strings.Contains(status.StateError, "exit code 3") {
		t.Errorf("state error = %q, want the exit code", status.StateError)
	}
}

func TestOnFailureRestartsCrashedInstance(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartModeOnFailure})
	h.restartAtOnce()
	metrics := &fakeMetrics{}
	h.manager.metrics = metrics
	h.start(t)

	h.crash(t)
	h.waitForVMMs(t, 2)
	status := h.waitForState(t, types.InstanceStateRunning)

	if status.RestartCount != 1 {
		t.Errorf("restart count = %d, want 1", status.RestartCount)
	}
	if h.manager.vmm(h.instance.ID) != h.starter.vmm() {
		t.Error("the restarted VMM is not supervised")
	}
	if metrics.restarts != 1 {
		t.Errorf("restarts recorded = %d, want 1", metrics.restarts)
	}
}

func TestOnFailureLeavesCleanExitStopped(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartModeOnFailure})
	h.restartAtOnce()
	h.start(t)

	h.exit(t, 0)
	h.waitForState(t, types.InstanceStateStopped)

	time.Sleep(50 * time.Millisecond)
	if n := h.starter.vmmCount(); n != 1 {
		t.Errorf("launched %d VMMs, want no restart after a clean exit", n)
	}
}

func TestAlwaysRestartsCleanExit(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartModeAlways})
	h.restartAtOnce()
	h.start(t)

	h.exit(t, 0)
	h.waitForVMMs(t, 2)
	h.waitForState(t, types.InstanceStateRunning)
}

func TestOnFailureGivesUpAfterMaxRetries(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartModeOnFailure, MaxRetries: 1})
	h.restartAtOnce()
	h.start(t)

	h.exit(t, 1)
	h.waitForVMMs(t, 2)
	h.waitForState(t, types.InstanceStateRunning)

	h.exit(t, 1)
	status := h.waitForState(t, types.InstanceStateFailed)

	if !strings.Contains(status.StateError, "gave up after 1 restart:") ||
		!strings.Contains(status.StateError, "exit code 1") {
		t.Errorf("state error = %q, want the give-up and its cause", status.StateError)
	}
}

// A restart that cannot start is an end like any other: it counts against
// the retry limit and backs off, rather than being retried in a hot loop or
// abandoned at the first try.
func TestFailedRestartIsRetried(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartModeOnFailure, MaxRetries: 2})
	h.restartAtOnce()
	h.start(t)

	h.starter.startErr = errors.New("no hypervisor today")
	h.crash(t)
	status := h.waitForState(t, types.InstanceStateFailed)

	if status.RestartCount != 2 {
		t.Errorf("restart count = %d, want both retries used", status.RestartCount)
	}
	if !strings.Contains(status.StateError, "gave up after 2 restarts") ||
		!strings.Contains(status.StateError, "no hypervisor today") {
		t.Errorf("state error = %q, want the give-up and why the restart failed", status.StateError)
	}
}

// restartingHarness returns a harness whose instance crashed and is waiting
// for a restart that is not due for a long time.
func restartingHarness(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartModeAlways})
	h.manager.restartWait = func(time.Time) time.Duration { return time.Hour }
	h.start(t)

	h.crash(t)
	status := h.waitForState(t, types.InstanceStateRestarting)
	if status.NextRestartAt.IsZero() || status.StateError == "" {
		t.Fatalf("status = %+v, want when it restarts and why", status)
	}
	return h
}

func TestStopCancelsPendingRestart(t *testing.T) {
	h := restartingHarness(t)

	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if status := h.status(t); status.State != types.InstanceStateStopped {
		t.Errorf("state = %s, want Stopped", status.State)
	}
	if len(h.manager.restarts) != 0 {
		t.Error("the restart is still pending")
	}
	if instance, _ := h.definitions.Instance(h.instance.ID); !instance.StoppedByUser {
		t.Error("the stop is not recorded as a user's")
	}
}

// Starting a Restarting instance starts it now, and as a fresh start: its
// restarts in a row are forgotten.
func TestStartDuringRestartStartsNow(t *testing.T) {
	h := restartingHarness(t)

	h.start(t)

	status := h.status(t)
	if status.State != types.InstanceStateRunning || status.RestartCount != 0 {
		t.Errorf("status = %s with %d restarts, want Running with none", status.State, status.RestartCount)
	}
	if len(h.manager.restarts) != 0 {
		t.Error("the restart is still pending")
	}
}

func TestDeleteCancelsPendingRestart(t *testing.T) {
	h := restartingHarness(t)

	if err := h.manager.Delete(t.Context(), h.instance, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(h.manager.restarts) != 0 {
		t.Error("the restart is still pending")
	}
}

// A daemon shutting down leaves a Restarting instance for the next one.
func TestCloseLeavesRestartForNextDaemon(t *testing.T) {
	h := restartingHarness(t)

	h.manager.Close()

	if status := h.status(t); status.State != types.InstanceStateRestarting {
		t.Errorf("state = %s, want Restarting", status.State)
	}
}

func TestRecoverReschedulesRestart(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartModeAlways})
	err := h.manager.writeStatus(types.InstanceStatus{
		InstanceID:    h.instance.ID,
		State:         types.InstanceStateRestarting,
		RestartCount:  2,
		NextRestartAt: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	h.manager.Recover(context.Background())

	h.waitForVMMs(t, 1)
	status := h.waitForState(t, types.InstanceStateRunning)
	if status.RestartCount != 2 {
		t.Errorf("restart count = %d, want the count carried over", status.RestartCount)
	}
}

// A VMM that died while the daemon was down is an end the policy applies to,
// as if it had been seen to happen.
func TestRecoverAppliesPolicyToInstanceThatEnded(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartModeOnFailure})
	h.restartAtOnce()

	dead := deadPID(t)
	err := h.manager.writeStatus(types.InstanceStatus{
		InstanceID: h.instance.ID,
		State:      types.InstanceStateRunning,
		VMMPID:     &dead,
		StartedAt:  time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	h.manager.Recover(context.Background())

	h.waitForVMMs(t, 1)
	h.waitForState(t, types.InstanceStateRunning)
}
