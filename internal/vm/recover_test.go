// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// startAdoptable launches a real process to play a VMM left running by a
// previous daemon, and makes it the one manager adopts for its PID. Adoption by
// pidfd is process.Attach's business and tested there; here it is only
// what recovery does with the result.
func startAdoptable(t *testing.T, manager *Manager) *process.Process {
	t.Helper()

	vmm, err := process.Start(exec.CommandContext(t.Context(), "sleep", "60"))
	if err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	t.Cleanup(func() {
		manager.Close()
		vmm.Terminate()
	})

	next := manager.attach
	manager.attach = func(pid int, arg string) (*process.Process, error) {
		if pid == vmm.PID() {
			return vmm, nil
		}
		return next(pid, arg)
	}

	return vmm
}

func TestRecoverAdoptsLiveInstance(t *testing.T) {
	manager, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	instance := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, manager)
	pid := vmm.PID()

	err := manager.writeStatus(types.InstanceStatus{
		InstanceID: instance.ID,
		State:      types.InstanceStateRunning,
		VMMPID:     &pid,
	})
	if err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	manager.Recover(ctx)

	status, err := manager.Status(instance)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if status.State != types.InstanceStateRunning {
		t.Errorf("state = %s, want Running for a VMM that is still alive", status.State)
	}
	if len(hostNetwork.removedTAPs) != 0 {
		t.Errorf("released host resources for a live instance: %v", hostNetwork.removedTAPs)
	}
	if manager.vmm(instance.ID) != vmm {
		t.Error("adopted VMM is not supervised")
	}
}

// Recovery sets up the networks of adopted instances again, and only
// theirs, so that the host network knows their bridges.
func TestRecoverSetsUpTheNetworksOfAdoptedInstances(t *testing.T) {
	manager, definitions, hostNetwork := newTestManager(t)

	instance := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, manager)
	pid := vmm.PID()
	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: instance.ID, State: types.InstanceStateRunning, VMMPID: &pid,
	}); err != nil {
		t.Fatal(err)
	}
	// A stopped instance on another network does not need its bridge.
	stopped := seedInstance(t, definitions, "idle")
	stopped.NetworkName = "quiet"
	definitions.instances["idle"] = stopped
	definitions.networks["quiet"] = types.Network{Name: "quiet", Bridge: "dicer-quiet", Subnet: "10.2.0.0/24", Gateway: "10.2.0.1"}

	manager.Recover(context.Background())

	if want := []string{definitions.networks["default"].Bridge}; !slices.Equal(hostNetwork.setUpBridges, want) {
		t.Errorf("bridges set up = %q, want %q", hostNetwork.setUpBridges, want)
	}
}

// TestAdoptedVMMCrashFailsInstance checks that an adopted VMM is watched
// like one the daemon started: its death is noticed when it happens.
func TestAdoptedVMMCrashFailsInstance(t *testing.T) {
	manager, definitions, hostNetwork := newTestManager(t)

	instance := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, manager)
	pid := vmm.PID()
	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: instance.ID, State: types.InstanceStateRunning, VMMPID: &pid,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	manager.Recover(t.Context())

	vmm.Terminate()

	h := &harness{manager: manager, hostNetwork: hostNetwork, instance: instance}
	status := h.waitForState(t, types.InstanceStateFailed)
	if !strings.Contains(status.StateError, "exited unexpectedly") {
		t.Errorf("state error = %q, want an unexpected exit", status.StateError)
	}
	if len(hostNetwork.removedTAPs) != 1 {
		t.Errorf("removed TAPs = %v, want the instance's", hostNetwork.removedTAPs)
	}
}

// TestRecoverKillsVMMOfInterruptedStop covers the daemon dying part way
// through a stop: the VMM may still be running, and must not be adopted as
// if nothing had happened.
func TestRecoverKillsVMMOfInterruptedStop(t *testing.T) {
	manager, definitions, hostNetwork := newTestManager(t)

	instance := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, manager)
	pid := vmm.PID()
	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: instance.ID, State: types.InstanceStateStopping, VMMPID: &pid,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	manager.Recover(t.Context())

	select {
	case <-vmm.Done():
	default:
		t.Error("the VMM of an interrupted stop is still running")
	}

	status, err := manager.Status(instance)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != types.InstanceStateFailed || !strings.Contains(status.StateError, "interrupted") {
		t.Errorf("state = %s (%q), want Failed as interrupted", status.State, status.StateError)
	}
	if len(hostNetwork.removedTAPs) != 1 {
		t.Errorf("removed TAPs = %v, want the instance's", hostNetwork.removedTAPs)
	}
}

func TestRecoverCleansUpDeadInstance(t *testing.T) {
	manager, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	instance := seedInstance(t, definitions, "web")

	// A PID that is not running: the daemon and its VMM both died.
	deadPID := deadPID(t)
	err := manager.writeStatus(types.InstanceStatus{
		InstanceID: instance.ID,
		State:      types.InstanceStateRunning,
		VMMPID:     &deadPID,
	})
	if err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	manager.Recover(ctx)

	status, err := manager.Status(instance)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	// A VMM that died while nobody was watching is still a failure, and
	// must not be passed off as a clean stop.
	if status.State != types.InstanceStateFailed || !strings.Contains(status.StateError, "not running") {
		t.Errorf("state = %s (%q), want Failed as having died while dicerd was down",
			status.State, status.StateError)
	}

	// Cleanup must reach the host layer, deriving the TAP from the instance
	// ID rather than reading back a recorded resource list.
	if len(hostNetwork.removedTAPs) != 1 || hostNetwork.removedTAPs[0] != instance.ID {
		t.Errorf("removed TAPs = %v, want [%s]", hostNetwork.removedTAPs, instance.ID)
	}
	if len(hostNetwork.tornDownBridges) != 1 {
		t.Errorf("torn down bridges = %v, want the bridge to go with the last instance", hostNetwork.tornDownBridges)
	}
}

// TestRecoverCleansUpInterruptedStart covers a crash partway through Start:
// the instance is recorded as Starting with no PID at all.
func TestRecoverCleansUpInterruptedStart(t *testing.T) {
	manager, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	instance := seedInstance(t, definitions, "web")
	forceState(t, manager, instance.ID, types.InstanceStateStarting)

	manager.Recover(ctx)

	status, err := manager.Status(instance)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if status.State != types.InstanceStateFailed || !strings.Contains(status.StateError, "start interrupted") {
		t.Errorf("state = %s (%q), want Failed as an interrupted start", status.State, status.StateError)
	}
	if len(hostNetwork.removedTAPs) != 1 {
		t.Errorf("removed TAPs = %v, want cleanup to run for an interrupted start", hostNetwork.removedTAPs)
	}
}

// TestRecoverAfterReboot checks the case that needs no work at all: a reboot
// clears the runtime directory, so everything reads as Stopped and recovery
// must not go poking at host resources.
func TestRecoverAfterReboot(t *testing.T) {
	manager, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	seedInstance(t, definitions, "web")
	seedInstance(t, definitions, "db")

	manager.Recover(ctx)

	if len(hostNetwork.removedTAPs) != 0 || len(hostNetwork.tornDownBridges) != 0 {
		t.Errorf("recovery touched host resources for instances that were not running: taps=%v bridges=%v",
			hostNetwork.removedTAPs, hostNetwork.tornDownBridges)
	}
}

// TestRecoverKeepsBridgeWhileAnotherInstanceRuns guards against tearing a
// bridge out from under a VM that is still using it.
func TestRecoverKeepsBridgeWhileAnotherInstanceRuns(t *testing.T) {
	manager, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	dead := seedInstance(t, definitions, "dead")
	live := seedInstance(t, definitions, "live")

	deadPID := deadPID(t)
	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: dead.ID, State: types.InstanceStateRunning, VMMPID: &deadPID,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	livePID := startAdoptable(t, manager).PID()
	if err := manager.writeStatus(types.InstanceStatus{
		InstanceID: live.ID, State: types.InstanceStateRunning, VMMPID: &livePID,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	manager.Recover(ctx)

	if len(hostNetwork.tornDownBridges) != 0 {
		t.Errorf("tore down bridge %v while another instance was still running", hostNetwork.tornDownBridges)
	}
	if len(hostNetwork.removedTAPs) != 1 || hostNetwork.removedTAPs[0] != dead.ID {
		t.Errorf("removed TAPs = %v, want only the dead instance's", hostNetwork.removedTAPs)
	}
}

func TestRecoverReleasesOrphanedAllocations(t *testing.T) {
	manager, definitions, _ := newTestManager(t)
	ctx := context.Background()

	seedInstance(t, definitions, "web")

	// An allocation left behind by an instance that no longer exists.
	n, err := definitions.Network("default")
	if err != nil {
		t.Fatalf("get network: %v", err)
	}
	if _, err := manager.networks.Allocate(n, "id-ghost", ""); err != nil {
		t.Fatalf("allocate: %v", err)
	}

	manager.Recover(ctx)

	networks, ok := manager.networks.(*fakeNetworks)
	if !ok {
		t.Fatalf("networks is %T, want *fakeNetworks", manager.networks)
	}
	allocations := networks.allocations("default")
	for _, allocation := range allocations {
		if allocation.InstanceID == "id-ghost" {
			t.Errorf("orphaned allocation for %s survived recovery", allocation.InstanceID)
		}
	}
}

// deadPID returns a PID that is not running, by starting a process and
// reaping it.
func deadPID(t *testing.T) int {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}

	return cmd.ProcessState.Pid()
}

func TestStartOnBootOnlyStartsInstancesThatAskToBeRunning(t *testing.T) {
	manager, definitions, _ := newTestManager(t)
	ctx := context.Background()

	// None of these asks to be started at boot, so nothing should be
	// attempted -- a start would fail here anyway, since there is no
	// hypervisor, which is exactly what makes this assertion meaningful.
	seedInstance(t, definitions, "web")
	onFailure := seedInstance(t, definitions, "db")
	onFailure.Restart = types.RestartPolicy{Mode: types.RestartModeOnFailure}
	definitions.instances[onFailure.Name] = onFailure
	stopped := seedInstance(t, definitions, "cache")
	stopped.Restart = types.RestartPolicy{Mode: types.RestartModeUnlessStopped}
	stopped.StoppedByUser = true
	definitions.instances[stopped.Name] = stopped

	manager.StartOnBoot(ctx)

	for _, name := range []string{"web", "db", "cache"} {
		instance, err := definitions.Instance(name)
		if err != nil {
			t.Fatalf("get instance: %v", err)
		}
		status, err := manager.Status(instance)
		if err != nil {
			t.Fatalf("get runtime: %v", err)
		}
		if status.State != types.InstanceStateStopped {
			t.Errorf("%s state = %s, want Stopped", name, status.State)
		}
	}
}

// An instance that asks to be running comes back at boot even if its VMM
// died while the daemon was down: it would have been restarted had the
// daemon seen it go.
func TestStartOnBootStartsFailedInstance(t *testing.T) {
	for _, tt := range []struct {
		policy        types.RestartMode
		stoppedByUser bool
	}{
		{types.RestartModeUnlessStopped, false},
		// always overrides a user's stop.
		{types.RestartModeAlways, true},
	} {
		t.Run(string(tt.policy), func(t *testing.T) {
			h := newHarness(t)
			h.setRestart(t, types.RestartPolicy{Mode: tt.policy})
			h.instance.StoppedByUser = tt.stoppedByUser
			h.definitions.instances[h.instance.Name] = h.instance

			h.manager.fail(h.instance.ID, errors.New("hypervisor exited"))

			h.manager.StartOnBoot(t.Context())

			if status := h.status(t); status.State != types.InstanceStateRunning {
				t.Errorf("state = %s (%s), want Running", status.State, status.StateError)
			}
		})
	}
}
