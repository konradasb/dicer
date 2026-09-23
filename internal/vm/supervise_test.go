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

	"github.com/dicer-sh/dicer"
)

func TestVMMCrashFailsInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.crash(t)
	rt := h.waitForState(t, dicer.StateFailed)

	if !strings.Contains(rt.StateError, "exited unexpectedly") ||
		!strings.Contains(rt.StateError, "signal: killed") {
		t.Errorf("state error = %q, want the unexpected exit and its signal", rt.StateError)
	}
	if rt.HypervisorPID != nil || rt.HypervisorSocketPath != "" {
		t.Errorf("runtime still names the dead process: pid=%v socket=%q",
			rt.HypervisorPID, rt.HypervisorSocketPath)
	}
	if h.mgr.vmm(h.inst.ID) != nil {
		t.Error("the dead VMM is still registered")
	}

	// Host resources go with the VMM, not on the next request.
	if len(h.hostNetwork.removedTAPs) != 1 || h.hostNetwork.removedTAPs[0] != h.inst.ID {
		t.Errorf("removed TAPs = %v, want [%s]", h.hostNetwork.removedTAPs, h.inst.ID)
	}
	if len(h.hostNetwork.tornDownBridges) != 1 {
		t.Errorf("torn down bridges = %v, want the bridge to go with its last instance", h.hostNetwork.tornDownBridges)
	}

	// And a failed instance can simply be started again, even though the
	// dead VMM's sockets are still in the runtime directory it left behind:
	// a real hypervisor refuses to bind over them.
	stale := []string{
		filepath.Join(h.mgr.runtimeDir(h.inst.ID), hypervisorSocketFile),
		filepath.Join(h.mgr.runtimeDir(h.inst.ID), vsockSocketFile),
	}
	for _, path := range stale {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	h.start(t)
	if rt := h.runtime(t); rt.State != dicer.StateRunning {
		t.Errorf("state after restart = %s, want Running", rt.State)
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
	if err := h.mgr.Pause(t.Context(), h.inst); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	h.crash(t)
	h.waitForState(t, dicer.StateFailed)
}

func TestStopIsNotACrash(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	vmm := h.starter.vmm()

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-vmm.Done():
	default:
		t.Error("Stop returned before the VMM exited")
	}

	// Give a watcher that wrongly took the exit for a crash time to act.
	time.Sleep(50 * time.Millisecond)
	if rt := h.runtime(t); rt.State != dicer.StateStopped {
		t.Errorf("state = %s (%s), want Stopped", rt.State, rt.StateError)
	}
}

// TestStopKillsVMMThatIgnoresShutdown covers a VMM that does not exit when
// asked: Stop must not return while it is still running.
func TestStopKillsVMMThatIgnoresShutdown(t *testing.T) {
	h := newHarness(t)
	h.hv.onShutdown = nil
	h.mgr.shutdownTimeout = 10 * time.Millisecond
	h.start(t)
	vmm := h.starter.vmm()

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-vmm.Done():
	default:
		t.Error("Stop returned while the VMM was still running")
	}
	if rt := h.runtime(t); rt.State != dicer.StateStopped {
		t.Errorf("state = %s, want Stopped", rt.State)
	}
}

func TestForcedDeleteIsNotACrash(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	vmm := h.starter.vmm()

	if err := h.mgr.Delete(t.Context(), h.inst, true); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	select {
	case <-vmm.Done():
	default:
		t.Error("Delete returned before the VMM exited")
	}

	time.Sleep(50 * time.Millisecond)
	if rt := h.runtime(t); rt.State != dicer.StateStopped {
		t.Errorf("state = %s (%s), want no runtime state left", rt.State, rt.StateError)
	}
}

// TestOldVMMExitDoesNotTouchNewOne guards the identity check: a watcher that
// fires late, for a VMM that was stopped and replaced, must leave the new one
// alone.
func TestOldVMMExitDoesNotTouchNewOne(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.start(t)

	time.Sleep(50 * time.Millisecond)
	if rt := h.runtime(t); rt.State != dicer.StateRunning {
		t.Errorf("state = %s (%s), want Running", rt.State, rt.StateError)
	}
	if h.mgr.vmm(h.inst.ID) != h.starter.vmm() {
		t.Error("the new VMM is no longer registered")
	}
}

func TestRestoredVMMCrashFailsInstance(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "snap"); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.clearRuntime(h.inst.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "snap"); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	h.crash(t)
	h.waitForState(t, dicer.StateFailed)
}

// TestCloseStopsWatching checks that the daemon shutting down leaves its VMMs
// and their recorded state alone.
func TestCloseStopsWatching(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.mgr.Close()
	h.crash(t)
	<-h.starter.vmm().Done()

	time.Sleep(50 * time.Millisecond)
	if rt := h.runtime(t); rt.State != dicer.StateRunning {
		t.Errorf("state = %s, want Running: a closed manager watches nothing", rt.State)
	}
}
