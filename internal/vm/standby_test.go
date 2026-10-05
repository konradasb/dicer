// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// TestStandbyFreesTheHostAndStartResumes checks that standby ends the VMM,
// so that the instance holds no CPU or memory, and that a start resumes the
// guest frozen on disk rather than booting it afresh.
func TestStandbyFreesTheHostAndStartResumes(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	vmm := h.starter.vmm()

	if err := h.manager.Standby(t.Context(), h.instance); err != nil {
		t.Fatalf("Standby: %v", err)
	}

	status := h.status(t)
	if status.State != types.InstanceStateStandby {
		t.Errorf("state = %s, want %s", status.State, types.InstanceStateStandby)
	}
	if held := status.HeldResources(); held != (types.Resources{}) {
		t.Errorf("an instance on standby holds %s, want nothing", held)
	}
	select {
	case <-vmm.Done():
	default:
		t.Error("the VMM is still running")
	}
	for _, f := range []string{standbyFile, "vmstate"} {
		if _, err := os.Stat(filepath.Join(h.manager.standbyDir(h.instance), f)); err != nil {
			t.Errorf("standby is missing %s: %v", f, err)
		}
	}
	if _, ok := h.events.last(events.ActionStandby); !ok {
		t.Error("no standby event was recorded")
	}

	if err := h.manager.Start(t.Context(), h.instance); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if len(h.starter.restoredFrom) != 1 || h.starter.restoredFrom[0] != h.manager.standbyDir(h.instance) {
		t.Errorf("restored from %v, want the standby directory", h.starter.restoredFrom)
	}
	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("state = %s, want %s", status.State, types.InstanceStateRunning)
	}
	if _, err := os.Stat(h.manager.standbyDir(h.instance)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the standby is still there after the instance resumed")
	}
	if h.agent.clockSets != 1 || len(h.agent.identities) != 0 {
		t.Errorf("clock set %d times and %d identities given, want 1 and none", h.agent.clockSets, len(h.agent.identities))
	}
}

// TestStandbyOutlivesTheRuntimeStatus checks that an instance is still on
// standby once its runtime status is gone, as a host reboot takes it.
func TestStandbyOutlivesTheRuntimeStatus(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	if err := h.manager.Standby(t.Context(), h.instance); err != nil {
		t.Fatal(err)
	}

	if err := h.manager.removeRuntimeDir(h.instance.ID); err != nil {
		t.Fatal(err)
	}

	if status := h.status(t); status.State != types.InstanceStateStandby {
		t.Errorf("state = %s, want %s", status.State, types.InstanceStateStandby)
	}
}

// TestStandbyKeepsItsPortsAndVolumes checks that no other instance can take
// the host port or writable volume an instance on standby resumes with.
func TestStandbyKeepsItsPortsAndVolumes(t *testing.T) {
	tests := []struct {
		name  string
		share func(*types.InstanceSpec)
	}{
		{"host port", func(s *types.InstanceSpec) {
			s.Ports = []types.PortMapping{{HostPort: 8080, GuestPort: 80}}
		}},
		{"writable volume", func(s *types.InstanceSpec) {
			s.Mounts = []types.Mount{{Type: types.MountTypeVolume, Source: "data", Target: "/data"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			volumes := fakeVolumes{dir: t.TempDir()}
			h.manager.volumes = volumes
			h.definitions.volumes["data"] = types.Volume{ID: "vol-data", Name: "data"}
			disk := volumes.Path("vol-data")
			if err := os.MkdirAll(filepath.Dir(disk), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(disk, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			tt.share(&h.instance)
			h.definitions.instances[h.instance.Name] = h.instance
			h.start(t)
			if err := h.manager.Standby(t.Context(), h.instance); err != nil {
				t.Fatal(err)
			}

			other := seedInstance(t, h.definitions, "other")
			tt.share(&other)
			h.definitions.instances[other.Name] = other

			if err := h.manager.Start(t.Context(), other); !errors.Is(err, errdefs.ErrInvalidState) {
				t.Errorf("Start of an instance sharing the %s = %v, want ErrInvalidState", tt.name, err)
			}
		})
	}
}

// TestStopDiscardsStandby checks that stopping an instance on standby throws
// away its frozen guest, so that the next start boots it afresh.
func TestStopDiscardsStandby(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	if err := h.manager.Standby(t.Context(), h.instance); err != nil {
		t.Fatal(err)
	}

	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if status := h.status(t); status.State != types.InstanceStateStopped {
		t.Errorf("state = %s, want %s", status.State, types.InstanceStateStopped)
	}

	if err := h.manager.Start(t.Context(), h.instance); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.starter.restoredFrom) != 0 {
		t.Error("a stopped instance was resumed from a discarded standby")
	}
}

func TestStandbyRejections(t *testing.T) {
	t.Run("stopped instance", func(t *testing.T) {
		h := newHarness(t)

		if err := h.manager.Standby(t.Context(), h.instance); !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("Standby of a stopped instance = %v, want ErrInvalidState", err)
		}
	})

	t.Run("hypervisor without snapshots", func(t *testing.T) {
		h := newHarness(t)
		h.start(t)
		h.hv.capabilities = hypervisor.Capabilities{SupportsPause: true}

		if err := h.manager.Standby(t.Context(), h.instance); !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("Standby = %v, want ErrUnsupported", err)
		}
	})

	t.Run("instance on standby changed", func(t *testing.T) {
		h := newHarness(t)
		h.start(t)
		if err := h.manager.Standby(t.Context(), h.instance); err != nil {
			t.Fatal(err)
		}

		changed := h.instance
		changed.VCPUs++
		if err := h.manager.Update(t.Context(), changed); !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("Update of an instance on standby = %v, want ErrInvalidState", err)
		}
	})
}

// TestFailedStandbyKeepsTheGuestRunning checks that a standby that cannot
// freeze the guest leaves it running as it was, with nothing frozen.
func TestFailedStandbyKeepsTheGuestRunning(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	h.hv.snapshotErr = errors.New("out of disk")

	if err := h.manager.Standby(t.Context(), h.instance); err == nil {
		t.Fatal("Standby succeeded despite the hypervisor failing")
	}

	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("state = %s, want %s", status.State, types.InstanceStateRunning)
	}
	if h.hv.paused != 1 || h.hv.resumed != 1 {
		t.Errorf("paused %d times and resumed %d, want 1 and 1", h.hv.paused, h.hv.resumed)
	}
	if h.manager.onStandby(h.instance) {
		t.Error("a failed standby left a frozen guest behind")
	}
}
