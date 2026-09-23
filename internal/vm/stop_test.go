// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

// TestStopFailedInstance checks that a failed start can be put to rest: Stop
// on a Failed instance cleans up and leaves it Stopped, rather than refusing.
func TestStopFailedInstance(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")

	mgr.fail(inst.ID, errors.New("boot failed"))

	if err := mgr.Stop(t.Context(), inst); err != nil {
		t.Fatalf("Stop() of a failed instance = %v, want nil", err)
	}

	rt, err := mgr.Runtime(inst)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	if rt.State != types.StateStopped {
		t.Errorf("state = %s, want %s", rt.State, types.StateStopped)
	}
}

// A stop the client gives up on is still seen through: the host network is
// torn down as fully as for any other.
func TestStopOutlivesItsRequest(t *testing.T) {
	mgr, definitions, hostNetwork := newTestManager(t)
	inst := seedInstance(t, definitions, "web")
	mgr.fail(inst.ID, errors.New("boot failed"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := mgr.Stop(ctx, inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if n := hostNetwork.cancelledTeardowns.Load(); n > 0 {
		t.Errorf("%d network teardowns were asked for with a cancelled context", n)
	}
}
