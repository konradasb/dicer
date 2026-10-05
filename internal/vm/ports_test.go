// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"slices"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// withPorts gives the harness's instance ports, in its definition too.
func (h *harness) withPorts(ports ...types.PortMapping) {
	h.instance.Ports = ports
	h.definitions.instances[h.instance.Name] = h.instance
}

// seedRunning defines another instance, recorded as in state.
func (h *harness) seedRunning(t *testing.T, name string, state types.InstanceState, ports ...types.PortMapping) types.InstanceSpec {
	t.Helper()

	other := seedInstance(t, h.definitions, name)
	other.Ports = ports
	h.definitions.instances[name] = other

	if err := h.manager.writeStatus(types.InstanceStatus{InstanceID: other.ID, State: state}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	return other
}

func TestStartPublishesPortsAndStopUnpublishes(t *testing.T) {
	h := newHarness(t)
	h.withPorts(types.PortMapping{HostPort: 8080, GuestPort: 80})
	h.start(t)

	allocation, err := h.manager.Allocation(h.instance)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := h.hostNetwork.published[h.instance.ID]
	if !ok || got.ip != allocation.IP || !slices.Equal(got.ports, h.instance.Ports) {
		t.Fatalf("published = %+v, want %v at the instance's address %s", got, h.instance.Ports, allocation.IP)
	}

	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := h.hostNetwork.published[h.instance.ID]; ok {
		t.Error("ports are still published after stop")
	}
}

func TestStartWithoutPortsPublishesNothing(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	if len(h.hostNetwork.published) != 0 {
		t.Errorf("published = %+v, want nothing for an instance without ports", h.hostNetwork.published)
	}
}

func TestCrashUnpublishesPorts(t *testing.T) {
	h := newHarness(t)
	h.withPorts(types.PortMapping{HostPort: 8080, GuestPort: 80})
	h.start(t)

	h.crash(t)
	h.waitForState(t, types.InstanceStateFailed)

	if _, ok := h.hostNetwork.published[h.instance.ID]; ok {
		t.Error("ports are still published after the VMM crashed")
	}
}

func TestFailedPublishUndoesNetwork(t *testing.T) {
	h := newHarness(t)
	h.withPorts(types.PortMapping{HostPort: 22, GuestPort: 22})
	h.hostNetwork.publishErr = errors.New("host port is in use")

	if err := h.manager.Start(t.Context(), h.instance); err == nil {
		t.Fatal("Start succeeded although its ports could not be published")
	}
	if !slices.Contains(h.hostNetwork.removedTAPs, h.instance.ID) {
		t.Errorf("removed TAPs = %v, want the instance's TAP gone after the failed start", h.hostNetwork.removedTAPs)
	}
}

func TestStartRefusesPortHeldByAnotherInstance(t *testing.T) {
	for _, state := range []types.InstanceState{types.InstanceStateRunning, types.InstanceStatePaused, types.InstanceStateStarting, types.InstanceStateStopping} {
		t.Run(string(state), func(t *testing.T) {
			h := newHarness(t)
			h.withPorts(types.PortMapping{HostIP: "192.0.2.1", HostPort: 8080, GuestPort: 80})
			h.seedRunning(t, "db", state, types.PortMapping{HostPort: 8080, GuestPort: 5432})

			err := h.manager.Start(t.Context(), h.instance)
			if !errors.Is(err, errdefs.ErrInvalidState) {
				t.Fatalf("Start = %v, want a refusal for the port %s instance holds", err, state)
			}
			if len(h.hostNetwork.published) != 0 {
				t.Errorf("published = %+v, want nothing", h.hostNetwork.published)
			}
			// Refused at admission: the instance was never Starting, so
			// it is left as it was rather than Failed.
			if status := h.status(t); status.State != types.InstanceStateStopped {
				t.Errorf("state = %s, want %s", status.State, types.InstanceStateStopped)
			}
		})
	}
}

func TestStartAllowsPortsThatDoNotClash(t *testing.T) {
	h := newHarness(t)
	h.withPorts(types.PortMapping{HostPort: 8080, GuestPort: 80})
	// A different port, a different protocol, and the same port on an
	// instance that is not running.
	h.seedRunning(t, "db", types.InstanceStateRunning, types.PortMapping{HostPort: 5432, GuestPort: 5432})
	h.seedRunning(t, "dns", types.InstanceStateRunning, types.PortMapping{HostPort: 8080, GuestPort: 80, Protocol: "udp"})
	h.seedRunning(t, "old", types.InstanceStateStopped, types.PortMapping{HostPort: 8080, GuestPort: 80})

	h.start(t)
}

func TestRestoreSnapshotPublishesPorts(t *testing.T) {
	h := newHarness(t)
	h.withPorts(types.PortMapping{HostPort: 8080, GuestPort: 80})
	h.running(t)

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "good")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	h.stopped(t)

	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if _, ok := h.hostNetwork.published[h.instance.ID]; !ok {
		t.Error("ports were not published for the restored instance")
	}
}
