// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"

	"github.com/dicer-sh/dicer"
)

// seedVolumeHolder defines another instance mounting data as mode, recorded
// as in state.
func (h *harness) seedVolumeHolder(t *testing.T, state dicer.InstanceState, mode dicer.VolumeAccessMode) {
	t.Helper()

	other := seedInstance(t, h.definitions, "other")
	other.VolumeMounts = []dicer.VolumeMount{{VolumeName: "data", MountPath: "/data", AccessMode: mode}}
	h.definitions.instances[other.Name] = other

	if err := h.mgr.writeRuntime(dicer.InstanceStatus{InstanceID: other.ID, State: state}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}
}

func TestAdmitVolumeSharing(t *testing.T) {
	rwo, rox := dicer.AccessModeReadWriteOnce, dicer.AccessModeReadOnlyMany
	tests := []struct {
		name    string
		mine    dicer.VolumeAccessMode
		theirs  dicer.VolumeAccessMode
		state   dicer.InstanceState
		refused bool
	}{
		{"read-write beside running read-write", rwo, rwo, dicer.StateRunning, true},
		{"read-write beside starting read-write", rwo, rwo, dicer.StateStarting, true},
		{"read-write beside stopping read-write", rwo, rwo, dicer.StateStopping, true},
		{"read-write beside running read-only", rwo, rox, dicer.StateRunning, true},
		{"read-only beside paused read-write", rox, rwo, dicer.StatePaused, true},
		{"read-only beside running read-only", rox, rox, dicer.StateRunning, false},
		{"read-write beside stopped read-write", rwo, rwo, dicer.StateStopped, false},
		{"read-write beside failed read-write", rwo, rwo, dicer.StateFailed, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.inst.VolumeMounts = []dicer.VolumeMount{{VolumeName: "data", MountPath: "/data", AccessMode: tt.mine}}
			h.definitions.instances[h.inst.Name] = h.inst
			h.seedVolumeHolder(t, tt.state, tt.theirs)

			err := h.mgr.admit(h.inst, h.inst.Resources())
			if refused := errors.Is(err, dicer.ErrInvalidState); refused != tt.refused {
				t.Fatalf("admit = %v, want refused %v", err, tt.refused)
			}
			if tt.refused {
				if rt := h.runtime(t); rt.State != dicer.StateStopped {
					t.Errorf("state = %s, want a refused instance left %s", rt.State, dicer.StateStopped)
				}
			}
		})
	}
}
