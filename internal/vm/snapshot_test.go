// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

func TestCreateSnapshot(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	snap, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "before-upgrade")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	if snap.Name != "before-upgrade" || snap.InstanceID != h.inst.ID {
		t.Errorf("snapshot = %+v", snap)
	}
	if snap.HypervisorVersion != testHypervisorVersion {
		t.Errorf("hypervisor version = %q, want %q", snap.HypervisorVersion, testHypervisorVersion)
	}
	if snap.SizeBytes == 0 {
		t.Error("snapshot reports no size")
	}

	// A running guest must be paused while its memory and disk are copied,
	// and running again afterwards.
	if h.hv.paused != 1 || h.hv.resumed != 1 {
		t.Errorf("paused %d times and resumed %d, want 1 and 1", h.hv.paused, h.hv.resumed)
	}

	dir := h.mgr.snapshotDir(h.inst, "before-upgrade")
	for _, f := range []string{snapshotMetadataFile, overlayDiskFile, "vmstate"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("snapshot is missing %s: %v", f, err)
		}
	}

	// The disk copy must hold what the original held, holes included.
	want, err := os.ReadFile(h.overlay)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, overlayDiskFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("overlay copy is %d bytes, want %d identical bytes", len(got), len(want))
	}
}

// TestCreateSnapshotOfPausedInstance checks that a paused instance is left
// paused: the caller paused it, and a snapshot should not change that.
func TestCreateSnapshotOfPausedInstance(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	forceState(t, h.mgr, h.inst.ID, types.StatePaused)

	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "paused"); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	if h.hv.paused != 0 || h.hv.resumed != 0 {
		t.Errorf("paused %d times and resumed %d, want neither", h.hv.paused, h.hv.resumed)
	}
	if rt, _ := h.mgr.Runtime(h.inst); rt.State != types.StatePaused {
		t.Errorf("state = %s, want it left %s", rt.State, types.StatePaused)
	}
}

func TestCreateSnapshotGeneratesName(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	snap, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if snap.Name == "" {
		t.Fatal("no name was generated")
	}
	if _, err := h.mgr.GetSnapshot(h.inst, snap.Name); err != nil {
		t.Errorf("generated name %q cannot be read back: %v", snap.Name, err)
	}
}

func TestCreateSnapshotRejections(t *testing.T) {
	t.Run("stopped instance", func(t *testing.T) {
		h := newHarness(t)

		_, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "nope")
		if !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("CreateSnapshot of a stopped instance = %v, want ErrInvalidState", err)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)

		if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "twice"); err != nil {
			t.Fatal(err)
		}
		_, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "twice")
		if !errors.Is(err, errdefs.ErrExists) {
			t.Errorf("second CreateSnapshot = %v, want ErrExists", err)
		}
	})

	t.Run("invalid name", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)

		_, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "../escape")
		if !errors.Is(err, errdefs.ErrInvalidArgument) {
			t.Errorf("CreateSnapshot(../escape) = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("hypervisor without snapshots", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)
		h.hv.caps = hypervisor.Capabilities{SupportsPause: true}

		_, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "nope")
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("CreateSnapshot = %v, want ErrUnsupported", err)
		}
	})
}

// TestCreateSnapshotCleansUpAfterFailure checks that a failed snapshot
// leaves nothing behind to be mistaken for a usable one.
func TestCreateSnapshotCleansUpAfterFailure(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.hv.snapshotErr = errors.New("out of disk")

	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "doomed"); err == nil {
		t.Fatal("CreateSnapshot succeeded despite the hypervisor failing")
	}

	if _, err := os.Stat(h.mgr.snapshotDir(h.inst, "doomed")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a failed snapshot left its directory behind")
	}
	// The guest was paused for the attempt and must not be left that way.
	if h.hv.resumed != 1 {
		t.Errorf("resumed %d times, want 1", h.hv.resumed)
	}
}

func TestListAndDeleteSnapshots(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	for _, name := range []string{"first", "second"} {
		if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, name); err != nil {
			t.Fatalf("CreateSnapshot(%s): %v", name, err)
		}
	}

	snapshots, err := h.mgr.ListSnapshots(h.inst)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snapshots))
	}
	// Oldest first, so a listing reads as a history.
	if snapshots[0].CreatedAt.After(snapshots[1].CreatedAt) {
		t.Error("snapshots are not ordered oldest first")
	}

	if err := h.mgr.DeleteSnapshot(t.Context(), h.inst, "first"); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if _, err := h.mgr.GetSnapshot(h.inst, "first"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("GetSnapshot after delete = %v, want ErrNotFound", err)
	}
	if err := h.mgr.DeleteSnapshot(t.Context(), h.inst, "first"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("second DeleteSnapshot = %v, want ErrNotFound", err)
	}
}

func TestListSnapshotsOfInstanceWithNone(t *testing.T) {
	h := newHarness(t)

	snapshots, err := h.mgr.ListSnapshots(h.inst)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("got %d snapshots, want none", len(snapshots))
	}
}

func TestRestoreSnapshot(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "good"); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// The instance stops, and its disk moves on from the snapshot.
	if err := h.mgr.clearRuntime(h.inst.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.overlay, []byte("written after the snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.hv.resumed = 0

	if err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "good"); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	if len(h.starter.restoredFrom) != 1 ||
		h.starter.restoredFrom[0] != h.mgr.snapshotDir(h.inst, "good") {
		t.Errorf("restored from %v, want the snapshot's directory", h.starter.restoredFrom)
	}

	// A hypervisor restores a guest paused; the instance is only running
	// once it has been resumed.
	if h.hv.resumed != 1 {
		t.Errorf("resumed %d times, want 1", h.hv.resumed)
	}
	rt, err := h.mgr.Runtime(h.inst)
	if err != nil {
		t.Fatal(err)
	}
	if rt.State != types.StateRunning {
		t.Errorf("state = %s, want %s", rt.State, types.StateRunning)
	}
	if rt.HypervisorVersion != testHypervisorVersion {
		t.Errorf("hypervisor version = %q, want the snapshot's", rt.HypervisorVersion)
	}

	// The guest's memory expects the disk as it was, so the disk written
	// after the snapshot must be gone.
	got, err := os.ReadFile(h.overlay)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("written after the snapshot")) {
		t.Error("the overlay disk was not rolled back to the snapshot")
	}
}

// Restoring is a start by a user: an unless-stopped instance a user stopped
// and then restored is no longer one a user stopped.
func TestRestoreSnapshotIsAUserStart(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "good"); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "good"); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	if inst, _ := h.definitions.GetInstance(h.inst.ID); inst.StoppedByUser {
		t.Error("the restored instance is still recorded as stopped by a user")
	}
}

// TestRestoreSnapshotKeepsConsole checks that a restored guest keeps its
// console log: where the console is written is host-side configuration that a
// Firecracker snapshot does not carry, so restoring has to set it again.
func TestRestoreSnapshotKeepsConsole(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "snap"); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.clearRuntime(h.inst.ID); err != nil {
		t.Fatal(err)
	}

	if err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "snap"); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	want, err := h.mgr.logPath(h.inst, LogSourceGuest)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.starter.restoredConsole.Path; got != want {
		t.Errorf("restored with console %q, want %q -- the guest would log nowhere", got, want)
	}
}

func TestRestoreSnapshotRejections(t *testing.T) {
	t.Run("running instance", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)
		if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "snap"); err != nil {
			t.Fatal(err)
		}

		err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "snap")
		if !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("RestoreSnapshot of a running instance = %v, want ErrInvalidState", err)
		}
	})

	t.Run("unknown snapshot", func(t *testing.T) {
		h := newHarness(t)

		err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "ghost")
		if !errors.Is(err, errdefs.ErrNotFound) {
			t.Errorf("RestoreSnapshot = %v, want ErrNotFound", err)
		}
	})

	// A snapshot can only be restored by the hypervisor version that took
	// it, so a daemon that does not ship that version must say so rather
	// than restore it with another.
	t.Run("hypervisor version gone", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)
		if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "old"); err != nil {
			t.Fatal(err)
		}
		if err := h.mgr.clearRuntime(h.inst.ID); err != nil {
			t.Fatal(err)
		}

		h.starter.version = "v50.0.0"

		err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "old")
		if err == nil || !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("RestoreSnapshot = %v, want a complaint about the missing version", err)
		}
		if rt, _ := h.mgr.Runtime(h.inst); rt.State != types.StateStopped {
			t.Errorf("state = %s, want the instance left %s", rt.State, types.StateStopped)
		}
	})
}

// TestRestoreSnapshotFailureMarksFailed checks that a restore that dies
// half-way leaves the instance Failed with the reason, not Starting.
func TestRestoreSnapshotFailureMarksFailed(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "snap"); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.clearRuntime(h.inst.ID); err != nil {
		t.Fatal(err)
	}

	h.starter.restoreErr = errors.New("hypervisor refused")

	if err := h.mgr.RestoreSnapshot(t.Context(), h.inst, "snap"); err == nil {
		t.Fatal("RestoreSnapshot succeeded despite the hypervisor refusing")
	}

	rt, err := h.mgr.Runtime(h.inst)
	if err != nil {
		t.Fatal(err)
	}
	if rt.State != types.StateFailed {
		t.Errorf("state = %s, want %s", rt.State, types.StateFailed)
	}
	if rt.StateError == "" {
		t.Error("no reason was recorded")
	}
}

// TestCreateSnapshotRecordsWhatTheGuestHas checks that a snapshot records
// the resources the guest runs with, which a restore from an earlier
// snapshot can make differ from the definition.
func TestCreateSnapshotRecordsWhatTheGuestHas(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	if err := h.mgr.transitionWith(h.inst, types.StateRunning, func(rt *types.InstanceStatus) {
		rt.VCPUs, rt.MemoryBytes = 3, 3<<30
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "big")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if snap.VCPUs != 3 || snap.MemoryBytes != 3<<30 {
		t.Errorf("snapshot records %d vCPUs, %d bytes; want the guest's 3 vCPUs, %d bytes",
			snap.VCPUs, snap.MemoryBytes, int64(3<<30))
	}
}
