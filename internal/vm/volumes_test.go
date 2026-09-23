// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// dataMount mounts the volume data at /data.
func dataMount(readOnly bool) []types.Mount {
	return []types.Mount{{Type: types.MountVolume, Source: "data", Target: "/data", ReadOnly: readOnly}}
}

// seedVolumeHolder defines another instance mounting data, recorded as in
// state.
func (h *harness) seedVolumeHolder(t *testing.T, state types.InstanceState, readOnly bool) {
	t.Helper()

	other := seedInstance(t, h.definitions, "other")
	other.Mounts = dataMount(readOnly)
	h.definitions.instances[other.Name] = other

	if err := h.mgr.writeRuntime(types.InstanceStatus{InstanceID: other.ID, State: state}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}
}

func TestAdmitVolumeSharing(t *testing.T) {
	const rw, ro = false, true
	tests := []struct {
		name    string
		mine    bool // read-only
		theirs  bool
		state   types.InstanceState
		refused bool
	}{
		{"read-write beside running read-write", rw, rw, types.StateRunning, true},
		{"read-write beside starting read-write", rw, rw, types.StateStarting, true},
		{"read-write beside stopping read-write", rw, rw, types.StateStopping, true},
		{"read-write beside running read-only", rw, ro, types.StateRunning, true},
		{"read-only beside paused read-write", ro, rw, types.StatePaused, true},
		{"read-only beside running read-only", ro, ro, types.StateRunning, false},
		{"read-write beside stopped read-write", rw, rw, types.StateStopped, false},
		{"read-write beside failed read-write", rw, rw, types.StateFailed, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.inst.Mounts = dataMount(tt.mine)
			h.definitions.instances[h.inst.Name] = h.inst
			h.seedVolumeHolder(t, tt.state, tt.theirs)

			err := h.mgr.admit(h.inst, h.inst.Resources())
			if refused := errors.Is(err, errdefs.ErrInvalidState); refused != tt.refused {
				t.Fatalf("admit = %v, want refused %v", err, tt.refused)
			}
			if tt.refused {
				if rt := h.runtime(t); rt.State != types.StateStopped {
					t.Errorf("state = %s, want a refused instance left %s", rt.State, types.StateStopped)
				}
			}
		})
	}
}
