// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
)

// A workload that exits 0 is a clean end: the instance is Stopped, not
// Failed, and says how it ended.
func TestCleanExitStopsInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.exit(t, 0)
	rt := h.waitForState(t, dicer.StateStopped)

	if rt.ExitCode == nil || *rt.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", rt.ExitCode)
	}
	if rt.StateError != "" || rt.FinishedAt.IsZero() {
		t.Errorf("runtime = %+v, want no error and a finish time", rt)
	}
	if len(h.hostNetwork.removedTAPs) != 1 {
		t.Errorf("removed TAPs = %v, want the instance's network released", h.hostNetwork.removedTAPs)
	}
}

func TestNonZeroExitFailsInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.exit(t, 3)
	rt := h.waitForState(t, dicer.StateFailed)

	if rt.ExitCode == nil || *rt.ExitCode != 3 {
		t.Errorf("exit code = %v, want 3", rt.ExitCode)
	}
	if !strings.Contains(rt.StateError, "exit code 3") {
		t.Errorf("state error = %q, want the exit code", rt.StateError)
	}
}

func TestOnFailureRestartsCrashedInstance(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartOnFailure})
	h.restartAtOnce()
	metrics := &fakeMetrics{}
	h.mgr.metrics = metrics
	h.start(t)

	h.crash(t)
	h.waitForVMMs(t, 2)
	rt := h.waitForState(t, dicer.StateRunning)

	if rt.RestartCount != 1 {
		t.Errorf("restart count = %d, want 1", rt.RestartCount)
	}
	if h.mgr.vmm(h.inst.ID) != h.starter.vmm() {
		t.Error("the restarted VMM is not supervised")
	}
	if metrics.restarts != 1 {
		t.Errorf("restarts recorded = %d, want 1", metrics.restarts)
	}
}

func TestOnFailureLeavesCleanExitStopped(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartOnFailure})
	h.restartAtOnce()
	h.start(t)

	h.exit(t, 0)
	h.waitForState(t, dicer.StateStopped)

	time.Sleep(50 * time.Millisecond)
	if n := len(h.starter.vmms); n != 1 {
		t.Errorf("launched %d VMMs, want no restart after a clean exit", n)
	}
}

func TestAlwaysRestartsCleanExit(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartAlways})
	h.restartAtOnce()
	h.start(t)

	h.exit(t, 0)
	h.waitForVMMs(t, 2)
	h.waitForState(t, dicer.StateRunning)
}

func TestOnFailureGivesUpAfterMaxRetries(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartOnFailure, MaxRetries: 1})
	h.restartAtOnce()
	h.start(t)

	h.exit(t, 1)
	h.waitForVMMs(t, 2)
	h.waitForState(t, dicer.StateRunning)

	h.exit(t, 1)
	rt := h.waitForState(t, dicer.StateFailed)

	if !strings.Contains(rt.StateError, "gave up after 1 restart:") ||
		!strings.Contains(rt.StateError, "exit code 1") {
		t.Errorf("state error = %q, want the give-up and its cause", rt.StateError)
	}
}

// A restart that cannot start is an end like any other: it counts against
// the retry limit and backs off, rather than being retried in a hot loop or
// abandoned at the first try.
func TestFailedRestartIsRetried(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartOnFailure, MaxRetries: 2})
	h.restartAtOnce()
	h.start(t)

	h.starter.startErr = errors.New("no hypervisor today")
	h.crash(t)
	rt := h.waitForState(t, dicer.StateFailed)

	if rt.RestartCount != 2 {
		t.Errorf("restart count = %d, want both retries used", rt.RestartCount)
	}
	if !strings.Contains(rt.StateError, "gave up after 2 restarts") ||
		!strings.Contains(rt.StateError, "no hypervisor today") {
		t.Errorf("state error = %q, want the give-up and why the restart failed", rt.StateError)
	}
}

// restartingHarness returns a harness whose instance crashed and is waiting
// for a restart that is not due for a long time.
func restartingHarness(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartAlways})
	h.mgr.restartWait = func(time.Time) time.Duration { return time.Hour }
	h.start(t)

	h.crash(t)
	rt := h.waitForState(t, dicer.StateRestarting)
	if rt.NextRestartAt.IsZero() || rt.StateError == "" {
		t.Fatalf("runtime = %+v, want when it restarts and why", rt)
	}
	return h
}

func TestStopCancelsPendingRestart(t *testing.T) {
	h := restartingHarness(t)

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if rt := h.runtime(t); rt.State != dicer.StateStopped {
		t.Errorf("state = %s, want Stopped", rt.State)
	}
	if len(h.mgr.restarts) != 0 {
		t.Error("the restart is still pending")
	}
	if inst, _ := h.definitions.GetInstance(h.inst.ID); !inst.StoppedByUser {
		t.Error("the stop is not recorded as a user's")
	}
}

// Starting a Restarting instance starts it now, and as a fresh start: its
// restarts in a row are forgotten.
func TestStartDuringRestartStartsNow(t *testing.T) {
	h := restartingHarness(t)

	h.start(t)

	rt := h.runtime(t)
	if rt.State != dicer.StateRunning || rt.RestartCount != 0 {
		t.Errorf("runtime = %s with %d restarts, want Running with none", rt.State, rt.RestartCount)
	}
	if len(h.mgr.restarts) != 0 {
		t.Error("the restart is still pending")
	}
}

func TestDeleteCancelsPendingRestart(t *testing.T) {
	h := restartingHarness(t)

	if err := h.mgr.Delete(t.Context(), h.inst, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(h.mgr.restarts) != 0 {
		t.Error("the restart is still pending")
	}
}

// A daemon shutting down leaves a Restarting instance for the next one.
func TestCloseLeavesRestartForNextDaemon(t *testing.T) {
	h := restartingHarness(t)

	h.mgr.Close()

	if rt := h.runtime(t); rt.State != dicer.StateRestarting {
		t.Errorf("state = %s, want Restarting", rt.State)
	}
}

func TestRecoverReschedulesRestart(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartAlways})
	err := h.mgr.writeRuntime(dicer.InstanceStatus{
		InstanceID:    h.inst.ID,
		State:         dicer.StateRestarting,
		RestartCount:  2,
		NextRestartAt: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.mgr.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	h.waitForVMMs(t, 1)
	rt := h.waitForState(t, dicer.StateRunning)
	if rt.RestartCount != 2 {
		t.Errorf("restart count = %d, want the count carried over", rt.RestartCount)
	}
}

// A VMM that died while the daemon was down is an end the policy applies to,
// as if it had been seen to happen.
func TestRecoverAppliesPolicyToInstanceThatEnded(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartOnFailure})
	h.restartAtOnce()

	dead := deadPID(t)
	err := h.mgr.writeRuntime(dicer.InstanceStatus{
		InstanceID:    h.inst.ID,
		State:         dicer.StateRunning,
		HypervisorPID: &dead,
		StartedAt:     time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.mgr.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	h.waitForVMMs(t, 1)
	h.waitForState(t, dicer.StateRunning)
}
