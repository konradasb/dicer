// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
)

// TestForkOfMemorySnapshotRunsAsItself checks that a fork resumes the
// snapshot's guest on a TAP device of its own, and that the guest is given
// the fork's identity before anything it sends can reach the network: it
// wakes with the address of the instance it is a copy of, which is running.
func TestForkOfMemorySnapshotRunsAsItself(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
	if err != nil {
		t.Fatal(err)
	}

	fork := forkOf(snapshot.Instance, "copy")
	if err := h.manager.ForkSnapshot(t.Context(), snapshot, fork); err != nil {
		t.Fatalf("ForkSnapshot: %v", err)
	}

	status, err := h.manager.Status(fork)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != types.InstanceStateRunning {
		t.Errorf("fork is %s, want %s", status.State, types.InstanceStateRunning)
	}
	if got, want := h.starter.restoredSpec.TAPDevice, network.TAPName(fork.ID); got != want {
		t.Errorf("restored on TAP %q, want the fork's own %q", got, want)
	}
	// The guest keeps the vsock context ID it was snapshotted with.
	if status.VsockCID != vsockCID(h.instance.ID) {
		t.Errorf("vsock CID = %d, want the snapshot's guest's %d", status.VsockCID, vsockCID(h.instance.ID))
	}
	assertSameFile(t, h.manager.overlayDiskPath(fork), h.manager.snapshotOverlayDiskPath(snapshot))

	allocation, err := h.manager.Allocation(fork)
	if err != nil {
		t.Fatal(err)
	}
	if allocation.IP == snapshot.IP || allocation.MAC == snapshot.MAC {
		t.Errorf("fork's address %s %s is the snapshot's", allocation.IP, allocation.MAC)
	}
	if len(h.agent.identities) != 1 {
		t.Fatalf("the guest was given %d identities, want 1", len(h.agent.identities))
	}
	identity := h.agent.identities[0]
	if identity.GetHostname() != "copy" || identity.GetInterfaces()[0].GetMac() != allocation.MAC ||
		identity.GetInterfaces()[0].GetAddresses()[0] != allocation.IP+"/24" {
		t.Errorf("identity = %v, want the fork's hostname and address %s %s", identity, allocation.IP, allocation.MAC)
	}
	if h.agent.connectedForIdentity {
		t.Error("the fork could reach the network before it had its own identity")
	}
	if h.hostNetwork.disconnected[fork.ID] {
		t.Error("the fork's TAP device was left off its bridge")
	}
	if h.agent.clockSets != 1 {
		t.Errorf("the guest's clock was set %d times, want 1", h.agent.clockSets)
	}
}

func TestForkOfDiskSnapshotIsStopped(t *testing.T) {
	h := newHarness(t)
	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "cold")
	if err != nil {
		t.Fatal(err)
	}

	fork := forkOf(snapshot.Instance, "copy")
	if err := h.manager.ForkSnapshot(t.Context(), snapshot, fork); err != nil {
		t.Fatalf("ForkSnapshot: %v", err)
	}

	if status, _ := h.manager.Status(fork); status.State != types.InstanceStateStopped {
		t.Errorf("fork is %s, want %s", status.State, types.InstanceStateStopped)
	}
	if h.starter.vmmCount() != 0 {
		t.Error("forking a disk snapshot started a VMM")
	}
	assertSameFile(t, h.manager.overlayDiskPath(fork), h.manager.snapshotOverlayDiskPath(snapshot))
}

func TestFailedForkLeavesNoInstance(t *testing.T) {
	tests := []struct {
		name        string
		identityErr error
		want        error
	}{
		{"guest refuses its identity", errors.New("netlink: permission denied"), nil},
		// A snapshot taken before agents could take another identity.
		{"agent too old", grpcstatus.Error(codes.Unimplemented, "unknown method SetIdentity"), errdefs.ErrInvalidState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.running(t)
			snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
			if err != nil {
				t.Fatal(err)
			}
			h.agent.identityErr = tt.identityErr

			err = h.manager.ForkSnapshot(t.Context(), snapshot, forkOf(snapshot.Instance, "copy"))
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Errorf("ForkSnapshot = %v, want a failure (%v)", err, tt.want)
			}
			if _, err := h.definitions.Instance("copy"); !errors.Is(err, errdefs.ErrNotFound) {
				t.Errorf("the failed fork is still defined: %v", err)
			}
		})
	}
}

// TestRestoreSetsTheGuestsClock checks that a restored guest is told the
// time, which stood still in the snapshot, and keeps its own identity.
func TestRestoreSetsTheGuestsClock(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
	if err != nil {
		t.Fatal(err)
	}
	h.stopped(t)

	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	if h.agent.clockSets != 1 || len(h.agent.identities) != 0 {
		t.Errorf("clock set %d times and %d identities given, want 1 and none", h.agent.clockSets, len(h.agent.identities))
	}
	if len(h.hostNetwork.disconnected) != 0 {
		t.Error("a restore into the snapshot's own instance detached its TAP device")
	}
}

// TestForkOfRunningInstanceRunsBesideIt checks that forking a running
// instance pauses it only while its memory is written, resumes the copy as
// itself, and keeps no snapshot of it.
func TestForkOfRunningInstanceRunsBesideIt(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	fork := forkOf(h.instance, "copy")
	if err := h.manager.ForkInstance(t.Context(), h.instance, fork); err != nil {
		t.Fatalf("ForkInstance: %v", err)
	}

	if status, _ := h.manager.Status(fork); status.State != types.InstanceStateRunning {
		t.Errorf("fork is %s, want %s", status.State, types.InstanceStateRunning)
	}
	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("source is %s, want %s", status.State, types.InstanceStateRunning)
	}
	// The source is resumed after its memory is written, and the fork as
	// it is restored.
	if h.hv.paused != 1 || h.hv.resumed != 2 {
		t.Errorf("guests paused %d times and resumed %d, want 1 and 2", h.hv.paused, h.hv.resumed)
	}
	assertSameFile(t, h.manager.overlayDiskPath(fork), h.overlay)
	if len(h.agent.identities) != 1 || h.agent.identities[0].GetHostname() != "copy" {
		t.Errorf("identities given = %v, want the fork's alone", h.agent.identities)
	}

	if snapshots := h.manager.Snapshots(); len(snapshots) != 0 {
		t.Errorf("forking kept snapshots %v", snapshots)
	}
	if staged, _ := os.ReadDir(filepath.Join(h.definitions.dir, "snapshots")); len(staged) != 0 {
		t.Errorf("forking left %d staged directories", len(staged))
	}
}

func TestForkOfStoppedInstanceIsStopped(t *testing.T) {
	h := newHarness(t)

	fork := forkOf(h.instance, "copy")
	if err := h.manager.ForkInstance(t.Context(), h.instance, fork); err != nil {
		t.Fatalf("ForkInstance: %v", err)
	}

	if status, _ := h.manager.Status(fork); status.State != types.InstanceStateStopped {
		t.Errorf("fork is %s, want %s", status.State, types.InstanceStateStopped)
	}
	if h.starter.vmmCount() != 0 {
		t.Error("forking a stopped instance started a VMM")
	}
	assertSameFile(t, h.manager.overlayDiskPath(fork), h.overlay)
}

// TestRefusedForkOfInstanceDefinesNothing checks that an instance that
// cannot be forked as it is leaves no fork behind.
func TestRefusedForkOfInstanceDefinesNothing(t *testing.T) {
	h := newHarness(t)
	h.instance.Mounts = []types.Mount{{Type: types.MountTypeVolume, Source: "data", Target: "/data"}}
	h.running(t)

	err := h.manager.ForkInstance(t.Context(), h.instance, forkOf(h.instance, "copy"))
	if !errors.Is(err, errdefs.ErrInvalidState) {
		t.Errorf("ForkInstance of an instance that can write to a volume = %v, want ErrInvalidState", err)
	}
	if _, err := h.definitions.Instance("copy"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("the refused fork is defined: %v", err)
	}
	if h.hv.paused != 0 {
		t.Error("the refused fork paused its source")
	}
}

// forkOf defines an instance named name as a fork of source, as the API
// does.
func forkOf(source types.InstanceSpec, name string) types.InstanceSpec {
	fork := source
	fork.ID, fork.Name = "id-"+name, name
	fork.StaticIP, fork.Ports = "", nil
	return fork
}
