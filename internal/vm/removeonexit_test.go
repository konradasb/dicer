// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
)

// setRemoveOnExit has the harness instance ask to be deleted when it stops,
// in the definition the manager reads as well as the harness's copy.
func (h *harness) setRemoveOnExit(t *testing.T) {
	t.Helper()

	h.inst.RemoveOnExit = true
	h.definitions.instances[h.inst.Name] = h.inst
}

// waitForRemoval waits until the instance is gone from the definitions. The
// delete runs after the stop that triggered it, so it is not there the
// instant the instance stops.
func (h *harness) waitForRemoval(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := h.definitions.GetInstance(h.inst.ID); errors.Is(err, dicer.ErrNotFound) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the instance was not deleted when it stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A guest that ends on its own takes the instance with it, which is what
// 'dicer run --rm' asks for.
func TestRemoveOnExitDeletesTheInstanceWhenItsGuestEnds(t *testing.T) {
	h := newHarness(t)
	h.setRemoveOnExit(t)
	h.start(t)

	h.exit(t, 0)
	h.waitForRemoval(t)
}

// A guest that failed is deleted too: --rm says what happens when it stops,
// not how it stopped.
func TestRemoveOnExitDeletesAFailedInstance(t *testing.T) {
	h := newHarness(t)
	h.setRemoveOnExit(t)
	h.start(t)

	h.exit(t, 1)
	h.waitForRemoval(t)
}

// Stopping it is stopping it, so it goes then too.
func TestRemoveOnExitDeletesAStoppedInstance(t *testing.T) {
	h := newHarness(t)
	h.setRemoveOnExit(t)
	h.start(t)

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.waitForRemoval(t)
}

// An instance its restart policy will start again has not finished with the
// host, and is not deleted out from under the restart.
func TestRemoveOnExitKeepsAnInstanceThatWillRestart(t *testing.T) {
	h := newHarness(t)
	h.setRemoveOnExit(t)
	h.setRestart(t, dicer.RestartPolicy{Mode: dicer.RestartAlways})
	h.restartAtOnce()
	h.start(t)

	h.exit(t, 0)

	// It comes back rather than going away.
	h.waitForVMMs(t, 2)
	if _, err := h.definitions.GetInstance(h.inst.ID); err != nil {
		t.Fatalf("a restarting instance was deleted: %v", err)
	}
}

// Without it, an instance that stops is left where it was.
func TestAnInstanceThatDidNotAskIsNotDeleted(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.exit(t, 0)
	h.waitForState(t, dicer.StateStopped)

	if _, err := h.definitions.GetInstance(h.inst.ID); err != nil {
		t.Errorf("an instance that did not ask to be deleted was: %v", err)
	}
}
