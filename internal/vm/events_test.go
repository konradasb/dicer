// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// waitForAction polls until action has been recorded.
func (h *harness) waitForAction(t *testing.T, action types.EventAction) types.Event {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if e, ok := h.events.last(action); ok {
			return e
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s event; recorded %v", action, h.events.actions())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Each operation on an instance records what it did, about that instance.
func TestLifecycleIsRecorded(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	created := types.InstanceSpec{ID: "new-id", Name: "new", ImageRef: "alpine"}
	if err := h.mgr.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}
	h.start(t)
	if err := h.mgr.Pause(ctx, h.inst); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Resume(ctx, h.inst); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Stop(ctx, h.inst); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Update(ctx, h.inst); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Delete(ctx, h.inst, false); err != nil {
		t.Fatal(err)
	}

	want := []types.EventAction{
		types.ActionCreated, types.ActionStarted, types.ActionPaused, types.ActionResumed,
		types.ActionStopped, types.ActionUpdated, types.ActionDeleted,
	}
	if got := h.events.actions(); !slices.Equal(got, want) {
		t.Errorf("recorded %v, want %v", got, want)
	}

	// Every event says what happened, not only that something did.
	if bare := h.events.undescribed(); len(bare) > 0 {
		t.Errorf("events with no description: %+v", bare)
	}
	stopped, _ := h.events.last(types.ActionStopped)
	if !strings.HasPrefix(stopped.Message, "Stopped instance after running for ") {
		t.Errorf("stopped = %q, want how the guest was ended", stopped.Message)
	}

	first, _ := h.events.last(types.ActionCreated)
	if first.Kind != types.KindInstance || first.ID != "new-id" || first.Name != "new" {
		t.Errorf("created = %+v, want the new instance", first)
	}
	started, _ := h.events.last(types.ActionStarted)
	if started.ID != h.inst.ID || started.Name != h.inst.Name {
		t.Errorf("started = %+v, want it about %s", started, h.inst.Name)
	}
}

// A crash that is restarted is told as it happened: it died, and why; it
// will be restarted, when; it started again.
func TestCrashAndRestartAreRecorded(t *testing.T) {
	h := newHarness(t)
	h.setRestart(t, types.RestartPolicy{Mode: types.RestartAlways})
	h.restartAtOnce()
	h.start(t)

	h.crash(t)
	h.waitForVMMs(t, 2)
	h.waitForState(t, types.StateRunning)
	restarted, _ := h.events.last(types.ActionStarted)

	died, _ := h.events.last(types.ActionDied)
	if !strings.Contains(died.Message, "exited unexpectedly") {
		t.Errorf("died = %q, want why", died.Message)
	}
	restarting, _ := h.events.last(types.ActionRestarting)
	if restarting.Attributes["restart_count"] != "1" || restarting.Attributes["delay"] == "" {
		t.Errorf("restarting = %+v, want the restart count and delay", restarting.Attributes)
	}
	if restarted.Attributes["restart_count"] != "1" || !strings.HasPrefix(restarted.Message, "Restarted instance on ") ||
		!strings.Contains(restarted.Message, "(restart 1, policy always)") {
		t.Errorf("the restart's started = %+v, want it counted and said", restarted)
	}
	if bare := h.events.undescribed(); len(bare) > 0 {
		t.Errorf("events with no description: %+v", bare)
	}
}

func TestCleanExitIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.exit(t, 0)
	exited := h.waitForAction(t, types.ActionExited)
	if exited.Attributes["exit_code"] != "0" {
		t.Errorf("exited = %+v, want its exit code", exited.Attributes)
	}
	if _, ok := h.events.last(types.ActionDied); ok {
		t.Error("a clean exit was recorded as a death")
	}
}

func TestFailedStartIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.starter.startErr = errors.New("no hypervisor today")

	if err := h.mgr.Start(t.Context(), h.inst); err == nil {
		t.Fatal("Start succeeded")
	}
	died, ok := h.events.last(types.ActionDied)
	if !ok || !strings.HasPrefix(died.Message, "Failed to start instance: ") || !strings.Contains(died.Message, "no hypervisor today") {
		t.Errorf("died = %+v, want the failed start and why", died)
	}
}

// A health check reaching a verdict is recorded; starting is not a verdict.
func TestHealthVerdictsAreRecorded(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{}, false)
	h.start(t)

	unhealthy := h.waitForAction(t, types.ActionUnhealthy)
	if !strings.Contains(unhealthy.Message, "connection refused") {
		t.Errorf("unhealthy = %q, want what the probe said", unhealthy.Message)
	}

	probe.set(true, nil)
	healthy := h.waitForAction(t, types.ActionHealthy)
	if !strings.Contains(healthy.Message, "ok") {
		t.Errorf("healthy = %q, want what the probe answered", healthy.Message)
	}
}

func TestSnapshotsAreRecorded(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "before"); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.DeleteSnapshot(t.Context(), h.inst, "before"); err != nil {
		t.Fatal(err)
	}

	created, _ := h.events.last(types.ActionSnapshotCreated)
	deleted, _ := h.events.last(types.ActionSnapshotDeleted)
	if created.Attributes["snapshot"] != "before" || deleted.Attributes["snapshot"] != "before" {
		t.Errorf("snapshot events = %+v, %+v; want them to name the snapshot", created, deleted)
	}
}

func TestStopMessage(t *testing.T) {
	const grace, took, ran = 30 * time.Second, 1234 * time.Millisecond, 5*time.Minute + 3*time.Second
	tests := []struct {
		outcome stopOutcome
		want    string
	}{
		{stopGraceful, "Stopped instance after running for 5m3s: guest shut down gracefully in 1.2s"},
		{stopTimedOut, "Stopped instance after running for 5m3s: guest did not shut down within the 30s grace period; hypervisor shut down"},
		{stopNotRunning, "Instance was not running; nothing to stop"},
	}
	for _, tt := range tests {
		if got := stopMessage(tt.outcome, grace, took, ran); got != tt.want {
			t.Errorf("stopMessage(%v) =\n%q\nwant\n%q", tt.outcome, got, tt.want)
		}
	}
}

// An update says what it changed, and when that takes effect.
func TestUpdateMessage(t *testing.T) {
	before := types.InstanceSpec{ImageRef: "docker.io/library/nginx:1.27", VCPUs: 1, MemoryBytes: 256 << 20, DiskBytes: 10 << 30}
	after := before
	after.MemoryBytes = 512 << 20
	after.Env = map[string]string{"A": "1"}

	want := "Updated instance: memory 256 MiB → 512 MiB, environment changed; takes effect on next start"
	if got := updateMessage(before, after, types.StateStopped); got != want {
		t.Errorf("updateMessage =\n%q\nwant\n%q", got, want)
	}

	after = before
	after.Restart = types.RestartPolicy{Mode: types.RestartOnFailure, MaxRetries: 3}
	want = "Updated instance: restart policy no → on-failure:3; takes effect when the instance next ends"
	if got := updateMessage(before, after, types.StateRunning); got != want {
		t.Errorf("updateMessage =\n%q\nwant\n%q", got, want)
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		829 * time.Millisecond:                "829ms",
		1840 * time.Millisecond:               "1.8s",
		5*time.Minute + 3400*time.Millisecond: "5m3s",
	} {
		if got := duration(d); got != want {
			t.Errorf("duration(%v) = %q, want %q", d, got, want)
		}
	}
}
