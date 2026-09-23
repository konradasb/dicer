// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"slices"
	"testing"

	"github.com/dicer-sh/dicer"
)

// withPorts gives the harness's instance ports, in its definition too.
func (h *harness) withPorts(ports ...dicer.PortMapping) {
	h.inst.Ports = ports
	h.definitions.instances[h.inst.Name] = h.inst
}

// seedRunning defines another instance, recorded as in state.
func (h *harness) seedRunning(t *testing.T, name string, state dicer.InstanceState, ports ...dicer.PortMapping) dicer.InstanceSpec {
	t.Helper()

	other := seedInstance(t, h.definitions, name)
	other.Ports = ports
	h.definitions.instances[name] = other

	if err := h.mgr.writeRuntime(dicer.InstanceStatus{InstanceID: other.ID, State: state}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}
	return other
}

func TestStartPublishesPortsAndStopUnpublishes(t *testing.T) {
	h := newHarness(t)
	h.withPorts(dicer.PortMapping{HostPort: 8080, GuestPort: 80})
	h.start(t)

	alloc, err := h.mgr.Address(h.inst)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := h.hostNetwork.published[h.inst.ID]
	if !ok || got.ip != alloc.IP || !slices.Equal(got.ports, h.inst.Ports) {
		t.Fatalf("published = %+v, want %v at the instance's address %s", got, h.inst.Ports, alloc.IP)
	}

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := h.hostNetwork.published[h.inst.ID]; ok {
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
	h.withPorts(dicer.PortMapping{HostPort: 8080, GuestPort: 80})
	h.start(t)

	h.crash(t)
	h.waitForState(t, dicer.StateFailed)

	if _, ok := h.hostNetwork.published[h.inst.ID]; ok {
		t.Error("ports are still published after the VMM crashed")
	}
}

func TestFailedPublishUndoesNetwork(t *testing.T) {
	h := newHarness(t)
	h.withPorts(dicer.PortMapping{HostPort: 22, GuestPort: 22})
	h.hostNetwork.publishErr = errors.New("host port is in use")

	if err := h.mgr.Start(t.Context(), h.inst); err == nil {
		t.Fatal("Start succeeded although its ports could not be published")
	}
	if !slices.Contains(h.hostNetwork.removedTAPs, h.inst.ID) {
		t.Errorf("removed TAPs = %v, want the instance's TAP gone after the failed start", h.hostNetwork.removedTAPs)
	}
}

func TestStartRefusesPortHeldByAnotherInstance(t *testing.T) {
	for _, state := range []dicer.InstanceState{dicer.StateRunning, dicer.StatePaused, dicer.StateStarting, dicer.StateStopping} {
		t.Run(string(state), func(t *testing.T) {
			h := newHarness(t)
			h.withPorts(dicer.PortMapping{HostIP: "192.0.2.1", HostPort: 8080, GuestPort: 80})
			h.seedRunning(t, "db", state, dicer.PortMapping{HostPort: 8080, GuestPort: 5432})

			err := h.mgr.Start(t.Context(), h.inst)
			if !errors.Is(err, dicer.ErrInvalidState) {
				t.Fatalf("Start = %v, want a refusal for the port %s instance holds", err, state)
			}
			if len(h.hostNetwork.published) != 0 {
				t.Errorf("published = %+v, want nothing", h.hostNetwork.published)
			}
			// Refused at admission: the instance was never Starting, so
			// it is left as it was rather than Failed.
			if rt := h.runtime(t); rt.State != dicer.StateStopped {
				t.Errorf("state = %s, want %s", rt.State, dicer.StateStopped)
			}
		})
	}
}

func TestStartAllowsPortsThatDoNotClash(t *testing.T) {
	h := newHarness(t)
	h.withPorts(dicer.PortMapping{HostPort: 8080, GuestPort: 80})
	// A different port, a different protocol, and the same port on an
	// instance that is not running.
	h.seedRunning(t, "db", dicer.StateRunning, dicer.PortMapping{HostPort: 5432, GuestPort: 5432})
	h.seedRunning(t, "dns", dicer.StateRunning, dicer.PortMapping{HostPort: 8080, GuestPort: 80, Protocol: "udp"})
	h.seedRunning(t, "old", dicer.StateStopped, dicer.PortMapping{HostPort: 8080, GuestPort: 80})

	h.start(t)
}

func TestRestoreSnapshotPublishesPorts(t *testing.T) {
	h := newHarness(t)
	h.withPorts(dicer.PortMapping{HostPort: 8080, GuestPort: 80})
	h.running(t)

	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "good"); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if err := h.mgr.clearRuntime(h.inst.ID); err != nil {
		t.Fatal(err)
	}

	if err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "good"); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if _, ok := h.hostNetwork.published[h.inst.ID]; !ok {
		t.Error("ports were not published for the restored instance")
	}
}
