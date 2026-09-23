// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// gracefulHarness returns a harness whose guest answers a request to shut
// down by ending its VMM, as dicer-init does once the workload has stopped,
// if ends says so. It counts the requests, and the hypervisor shutdowns a
// stop falls back to.
func gracefulHarness(t *testing.T, ends bool) (h *harness, asked, forced *atomic.Int32) {
	t.Helper()

	h = newHarness(t)
	asked, forced = &atomic.Int32{}, &atomic.Int32{}

	h.mgr.shutdownGuest = func(context.Context, string) error {
		asked.Add(1)
		if ends {
			_ = h.starter.vmm().Kill()
		}
		return nil
	}
	h.hv.onShutdown = func() {
		forced.Add(1)
		_ = h.starter.vmm().Kill()
	}
	return h, asked, forced
}

// A stop asks the guest to shut down, and waits for it, rather than ending
// the VM under a workload that may be writing.
func TestStopShutsTheGuestDownGracefully(t *testing.T) {
	h, asked, forced := gracefulHarness(t, true)
	h.start(t)

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if asked.Load() != 1 || forced.Load() != 0 {
		t.Errorf("asked %d times, forced %d times; want the guest asked once and nothing forced",
			asked.Load(), forced.Load())
	}
	if rt := h.runtime(t); rt.State != types.StateStopped {
		t.Errorf("state = %s, want Stopped", rt.State)
	}
}

// A guest that does not shut down in its grace period is ended regardless.
func TestStopEndsAGuestThatIgnoresTheShutdown(t *testing.T) {
	h, asked, forced := gracefulHarness(t, false)
	h.mgr.stopGracePeriod = 20 * time.Millisecond
	h.start(t)

	started := time.Now()
	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if asked.Load() != 1 || forced.Load() != 1 {
		t.Errorf("asked %d times, forced %d times; want both once", asked.Load(), forced.Load())
	}
	if waited := time.Since(started); waited < h.mgr.stopGracePeriod {
		t.Errorf("Stop took %s, less than the grace period", waited)
	}
}

// A guest that cannot be asked -- its agent is too old, or unreachable -- is
// ended at once, not after waiting out a period it was never told about.
func TestStopEndsAGuestThatCannotBeAsked(t *testing.T) {
	h, _, forced := gracefulHarness(t, true)
	h.mgr.shutdownGuest = func(context.Context, string) error { return errors.New("unimplemented") }
	h.mgr.stopGracePeriod = time.Hour
	h.start(t)

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if forced.Load() != 1 {
		t.Errorf("forced %d times, want the VMM shut down directly", forced.Load())
	}
}

// A forced delete does not wait on the workload, as docker rm -f does not.
func TestForcedDeleteIsNotGraceful(t *testing.T) {
	h, asked, _ := gracefulHarness(t, true)
	h.start(t)

	if err := h.mgr.Delete(t.Context(), h.inst, true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if asked.Load() != 0 {
		t.Errorf("the guest was asked to shut down %d times, want none", asked.Load())
	}
}

// A paused guest cannot answer: it is not asked.
func TestPausedGuestIsNotAskedToShutDown(t *testing.T) {
	h, asked, forced := gracefulHarness(t, true)
	h.mgr.stopGracePeriod = time.Hour
	h.start(t)
	if err := h.mgr.Pause(t.Context(), h.inst); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if asked.Load() != 0 || forced.Load() != 1 {
		t.Errorf("asked %d times, forced %d times; want only forced", asked.Load(), forced.Load())
	}
}
