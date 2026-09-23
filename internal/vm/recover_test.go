// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// startAdoptable launches a real process to play a VMM left running by a
// previous daemon, and makes it the one mgr adopts for its PID. Adoption by
// pidfd is process.Attach's business and tested there; here it is only
// what recovery does with the result.
func startAdoptable(t *testing.T, mgr *Manager) *process.Process {
	t.Helper()

	vmm, err := process.Start(exec.CommandContext(t.Context(), "sleep", "60"))
	if err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	t.Cleanup(func() {
		mgr.Close()
		vmm.Terminate()
	})

	next := mgr.attach
	mgr.attach = func(pid int, arg string) (*process.Process, error) {
		if pid == vmm.PID() {
			return vmm, nil
		}
		return next(pid, arg)
	}

	return vmm
}

func TestRecoverAdoptsLiveInstance(t *testing.T) {
	mgr, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	inst := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, mgr)
	pid := vmm.PID()

	err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID:    inst.ID,
		State:         types.StateRunning,
		HypervisorPID: &pid,
	})
	if err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}

	if err := mgr.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	rt, err := mgr.Runtime(inst)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if rt.State != types.StateRunning {
		t.Errorf("state = %s, want Running for a VMM that is still alive", rt.State)
	}
	if len(hostNetwork.removedTAPs) != 0 {
		t.Errorf("released host resources for a live instance: %v", hostNetwork.removedTAPs)
	}
	if mgr.vmm(inst.ID) != vmm {
		t.Error("adopted VMM is not supervised")
	}
}

// TestAdoptedVMMCrashFailsInstance checks that an adopted VMM is watched
// like one the daemon started: its death is noticed when it happens.
func TestAdoptedVMMCrashFailsInstance(t *testing.T) {
	mgr, definitions, hostNetwork := newTestManager(t)

	inst := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, mgr)
	pid := vmm.PID()
	if err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID: inst.ID, State: types.StateRunning, HypervisorPID: &pid,
	}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}
	if err := mgr.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	vmm.Terminate()

	h := &harness{mgr: mgr, hostNetwork: hostNetwork, inst: inst}
	rt := h.waitForState(t, types.StateFailed)
	if !strings.Contains(rt.StateError, "exited unexpectedly") {
		t.Errorf("state error = %q, want an unexpected exit", rt.StateError)
	}
	if len(hostNetwork.removedTAPs) != 1 {
		t.Errorf("removed TAPs = %v, want the instance's", hostNetwork.removedTAPs)
	}
}

// TestRecoverKillsVMMOfInterruptedStop covers the daemon dying part way
// through a stop: the VMM may still be running, and must not be adopted as
// if nothing had happened.
func TestRecoverKillsVMMOfInterruptedStop(t *testing.T) {
	mgr, definitions, hostNetwork := newTestManager(t)

	inst := seedInstance(t, definitions, "web")
	vmm := startAdoptable(t, mgr)
	pid := vmm.PID()
	if err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID: inst.ID, State: types.StateStopping, HypervisorPID: &pid,
	}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}

	if err := mgr.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	select {
	case <-vmm.Done():
	default:
		t.Error("the VMM of an interrupted stop is still running")
	}

	rt, err := mgr.Runtime(inst)
	if err != nil {
		t.Fatal(err)
	}
	if rt.State != types.StateFailed || !strings.Contains(rt.StateError, "interrupted") {
		t.Errorf("state = %s (%q), want Failed as interrupted", rt.State, rt.StateError)
	}
	if len(hostNetwork.removedTAPs) != 1 {
		t.Errorf("removed TAPs = %v, want the instance's", hostNetwork.removedTAPs)
	}
}

func TestRecoverCleansUpDeadInstance(t *testing.T) {
	mgr, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	inst := seedInstance(t, definitions, "web")

	// A PID that is not running: the daemon and its VMM both died.
	deadPID := deadPID(t)
	err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID:    inst.ID,
		State:         types.StateRunning,
		HypervisorPID: &deadPID,
	})
	if err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}

	if err := mgr.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	rt, err := mgr.Runtime(inst)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	// A VMM that died while nobody was watching is still a failure, and
	// must not be passed off as a clean stop.
	if rt.State != types.StateFailed || !strings.Contains(rt.StateError, "not running") {
		t.Errorf("state = %s (%q), want Failed as having died while dicerd was down",
			rt.State, rt.StateError)
	}

	// Cleanup must reach the host layer, deriving the TAP from the instance
	// ID rather than reading back a recorded resource list.
	if len(hostNetwork.removedTAPs) != 1 || hostNetwork.removedTAPs[0] != inst.ID {
		t.Errorf("removed TAPs = %v, want [%s]", hostNetwork.removedTAPs, inst.ID)
	}
	if len(hostNetwork.tornDownBridges) != 1 {
		t.Errorf("torn down bridges = %v, want the bridge to go with the last instance", hostNetwork.tornDownBridges)
	}
}

// TestRecoverCleansUpInterruptedStart covers a crash partway through Start:
// the instance is recorded as Starting with no PID at all.
func TestRecoverCleansUpInterruptedStart(t *testing.T) {
	mgr, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	inst := seedInstance(t, definitions, "web")
	forceState(t, mgr, inst.ID, types.StateStarting)

	if err := mgr.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	rt, err := mgr.Runtime(inst)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if rt.State != types.StateFailed || !strings.Contains(rt.StateError, "start interrupted") {
		t.Errorf("state = %s (%q), want Failed as an interrupted start", rt.State, rt.StateError)
	}
	if len(hostNetwork.removedTAPs) != 1 {
		t.Errorf("removed TAPs = %v, want cleanup to run for an interrupted start", hostNetwork.removedTAPs)
	}
}

// TestRecoverAfterReboot checks the case that needs no work at all: a reboot
// clears the runtime directory, so everything reads as Stopped and recovery
// must not go poking at host resources.
func TestRecoverAfterReboot(t *testing.T) {
	mgr, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	seedInstance(t, definitions, "web")
	seedInstance(t, definitions, "db")

	if err := mgr.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if len(hostNetwork.removedTAPs) != 0 || len(hostNetwork.tornDownBridges) != 0 {
		t.Errorf("recovery touched host resources for instances that were not running: taps=%v bridges=%v",
			hostNetwork.removedTAPs, hostNetwork.tornDownBridges)
	}
}

// TestRecoverKeepsBridgeWhileAnotherInstanceRuns guards against tearing a
// bridge out from under a VM that is still using it.
func TestRecoverKeepsBridgeWhileAnotherInstanceRuns(t *testing.T) {
	mgr, definitions, hostNetwork := newTestManager(t)
	ctx := context.Background()

	dead := seedInstance(t, definitions, "dead")
	live := seedInstance(t, definitions, "live")

	deadPID := deadPID(t)
	if err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID: dead.ID, State: types.StateRunning, HypervisorPID: &deadPID,
	}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}

	livePID := startAdoptable(t, mgr).PID()
	if err := mgr.writeRuntime(types.InstanceStatus{
		InstanceID: live.ID, State: types.StateRunning, HypervisorPID: &livePID,
	}); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}

	if err := mgr.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if len(hostNetwork.tornDownBridges) != 0 {
		t.Errorf("tore down bridge %v while another instance was still running", hostNetwork.tornDownBridges)
	}
	if len(hostNetwork.removedTAPs) != 1 || hostNetwork.removedTAPs[0] != dead.ID {
		t.Errorf("removed TAPs = %v, want only the dead instance's", hostNetwork.removedTAPs)
	}
}

func TestRecoverReleasesOrphanedAllocations(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	ctx := context.Background()

	seedInstance(t, definitions, "web")

	// An allocation left behind by an instance that no longer exists.
	n, err := definitions.GetNetwork("default")
	if err != nil {
		t.Fatalf("get network: %v", err)
	}
	if _, err := mgr.addresses.Allocate(n, "id-ghost", ""); err != nil {
		t.Fatalf("allocate: %v", err)
	}

	if err := mgr.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	addrs, ok := mgr.addresses.(*fakeAddresses)
	if !ok {
		t.Fatalf("addresses is %T, want *fakeAddresses", mgr.addresses)
	}
	allocs := addrs.byNetwork["default"]
	for _, a := range allocs {
		if a.InstanceID == "id-ghost" {
			t.Errorf("orphaned allocation for %s survived recovery", a.InstanceID)
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
	mgr, definitions, _ := newTestManager(t)
	ctx := context.Background()

	// None of these asks to be started at boot, so nothing should be
	// attempted -- a start would fail here anyway, since there is no
	// hypervisor, which is exactly what makes this assertion meaningful.
	seedInstance(t, definitions, "web")
	onFailure := seedInstance(t, definitions, "db")
	onFailure.Restart = types.RestartPolicy{Mode: types.RestartOnFailure}
	definitions.instances[onFailure.Name] = onFailure
	stopped := seedInstance(t, definitions, "cache")
	stopped.Restart = types.RestartPolicy{Mode: types.RestartUnlessStopped}
	stopped.StoppedByUser = true
	definitions.instances[stopped.Name] = stopped

	mgr.StartOnBoot(ctx)

	for _, name := range []string{"web", "db", "cache"} {
		inst, err := definitions.GetInstance(name)
		if err != nil {
			t.Fatalf("get instance: %v", err)
		}
		rt, err := mgr.Runtime(inst)
		if err != nil {
			t.Fatalf("get runtime: %v", err)
		}
		if rt.State != types.StateStopped {
			t.Errorf("%s state = %s, want Stopped", name, rt.State)
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
		{types.RestartUnlessStopped, false},
		// always overrides a user's stop.
		{types.RestartAlways, true},
	} {
		t.Run(string(tt.policy), func(t *testing.T) {
			h := newHarness(t)
			h.setRestart(t, types.RestartPolicy{Mode: tt.policy})
			h.inst.StoppedByUser = tt.stoppedByUser
			h.definitions.instances[h.inst.Name] = h.inst

			h.mgr.fail(h.inst.ID, errors.New("hypervisor exited"))

			h.mgr.StartOnBoot(t.Context())

			if rt := h.runtime(t); rt.State != types.StateRunning {
				t.Errorf("state = %s (%s), want Running", rt.State, rt.StateError)
			}
		})
	}
}
