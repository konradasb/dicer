// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

func TestVMMCrashFailsInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.crash(t)
	status := h.waitForState(t, types.InstanceStateFailed)

	if !strings.Contains(status.StateError, "exited unexpectedly") ||
		!strings.Contains(status.StateError, "signal: killed") {
		t.Errorf("state error = %q, want the unexpected exit and its signal", status.StateError)
	}
	if status.VMMPID != nil || status.HypervisorSocketPath != "" {
		t.Errorf("status still names the dead process: pid=%v socket=%q",
			status.VMMPID, status.HypervisorSocketPath)
	}
	if h.manager.vmm(h.instance.ID) != nil {
		t.Error("the dead VMM is still registered")
	}

	// Host resources go with the VMM, not on the next request.
	if len(h.hostNetwork.removedTAPs) != 1 || h.hostNetwork.removedTAPs[0] != h.instance.ID {
		t.Errorf("removed TAPs = %v, want [%s]", h.hostNetwork.removedTAPs, h.instance.ID)
	}
	if len(h.hostNetwork.tornDownBridges) != 1 {
		t.Errorf("torn down bridges = %v, want the bridge to go with its last instance", h.hostNetwork.tornDownBridges)
	}

	// And a failed instance can simply be started again, even though the
	// dead VMM's sockets are still in the runtime directory it left behind:
	// a real hypervisor refuses to bind over them.
	stale := []string{
		filepath.Join(h.manager.runtimeDir(h.instance.ID), hypervisorSocketFile),
		filepath.Join(h.manager.runtimeDir(h.instance.ID), vsockSocketFile),
	}
	for _, path := range stale {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	h.start(t)
	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("state after restart = %s, want Running", status.State)
	}
	for _, path := range stale {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stale socket %s survived the restart", filepath.Base(path))
		}
	}
}

func TestPausedVMMCrashFailsInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	if err := h.manager.Pause(t.Context(), h.instance); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	h.crash(t)
	h.waitForState(t, types.InstanceStateFailed)
}

func TestStopIsNotACrash(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	vmm := h.starter.vmm()

	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-vmm.Done():
	default:
		t.Error("Stop returned before the VMM exited")
	}

	// Give a watcher that wrongly took the exit for a crash time to act.
	time.Sleep(50 * time.Millisecond)
	if status := h.status(t); status.State != types.InstanceStateStopped {
		t.Errorf("state = %s (%s), want Stopped", status.State, status.StateError)
	}
}

// TestStopKillsVMMThatIgnoresShutdown covers a VMM that does not exit when
// asked: Stop must not return while it is still running.
func TestStopKillsVMMThatIgnoresShutdown(t *testing.T) {
	h := newHarness(t)
	h.hv.onShutdown = nil
	h.manager.shutdownTimeout = 10 * time.Millisecond
	h.start(t)
	vmm := h.starter.vmm()

	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-vmm.Done():
	default:
		t.Error("Stop returned while the VMM was still running")
	}
	if status := h.status(t); status.State != types.InstanceStateStopped {
		t.Errorf("state = %s, want Stopped", status.State)
	}
}

func TestForcedDeleteIsNotACrash(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	vmm := h.starter.vmm()

	if err := h.manager.Delete(t.Context(), h.instance, true); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	select {
	case <-vmm.Done():
	default:
		t.Error("Delete returned before the VMM exited")
	}

	time.Sleep(50 * time.Millisecond)
	if status := h.status(t); status.State != types.InstanceStateStopped {
		t.Errorf("state = %s (%s), want no status left", status.State, status.StateError)
	}
}

// TestOldVMMExitDoesNotTouchNewOne guards the identity check: a watcher that
// fires late, for a VMM that was stopped and replaced, must leave the new one
// alone.
func TestOldVMMExitDoesNotTouchNewOne(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.start(t)

	time.Sleep(50 * time.Millisecond)
	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("state = %s (%s), want Running", status.State, status.StateError)
	}
	if h.manager.vmm(h.instance.ID) != h.starter.vmm() {
		t.Error("the new VMM is no longer registered")
	}
}

func TestRestoredVMMCrashFailsInstance(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
	if err != nil {
		t.Fatal(err)
	}
	h.stopped(t)
	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	h.crash(t)
	h.waitForState(t, types.InstanceStateFailed)
}

// TestCloseStopsWatching checks that the daemon shutting down leaves its VMMs
// and their recorded state alone.
func TestCloseStopsWatching(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.manager.Close()
	h.crash(t)
	<-h.starter.vmm().Done()

	time.Sleep(50 * time.Millisecond)
	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("state = %s, want Running: a closed manager watches nothing", status.State)
	}
}
