// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

func TestCreateMemorySnapshot(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "before-upgrade")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	if snapshot.Name != "before-upgrade" || snapshot.Kind != types.SnapshotKindMemory || snapshot.Instance.ID != h.instance.ID {
		t.Errorf("snapshot = %+v", snapshot)
	}
	if snapshot.HypervisorVersion != testHypervisorVersion {
		t.Errorf("hypervisor version = %q, want %q", snapshot.HypervisorVersion, testHypervisorVersion)
	}
	allocation, err := h.manager.Allocation(h.instance)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IP != allocation.IP || snapshot.MAC != allocation.MAC {
		t.Errorf("snapshot address = %s %s, want the guest's %s %s", snapshot.IP, snapshot.MAC, allocation.IP, allocation.MAC)
	}
	if snapshot.SizeBytes == 0 {
		t.Error("snapshot reports no size")
	}

	// A running guest must be paused while its memory and disk are copied,
	// and running again afterwards.
	if h.hv.paused != 1 || h.hv.resumed != 1 {
		t.Errorf("paused %d times and resumed %d, want 1 and 1", h.hv.paused, h.hv.resumed)
	}

	dir := h.manager.snapshotDir(snapshot)
	for _, f := range []string{overlayDiskFile, "vmstate"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("snapshot is missing %s: %v", f, err)
		}
	}
	assertSameFile(t, filepath.Join(dir, overlayDiskFile), h.overlay)

	event, ok := h.events.last(events.ActionCreated)
	if !ok || event.Kind != events.KindSnapshot || event.Attributes["paused_seconds"] == "" {
		t.Errorf("event = %+v, want a snapshot's creation saying how long the guest was paused", event)
	}
}

// TestCreateSnapshotOfPausedInstance checks that a paused instance is left
// paused: the caller paused it, and a snapshot should not change that.
func TestCreateSnapshotOfPausedInstance(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	forceState(t, h.manager, h.instance.ID, types.InstanceStatePaused)

	if _, err := h.manager.CreateSnapshot(t.Context(), h.instance, "paused"); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	if h.hv.paused != 0 || h.hv.resumed != 0 {
		t.Errorf("paused %d times and resumed %d, want neither", h.hv.paused, h.hv.resumed)
	}
	if status, _ := h.manager.Status(h.instance); status.State != types.InstanceStatePaused {
		t.Errorf("state = %s, want it left %s", status.State, types.InstanceStatePaused)
	}
}

func TestCreateDiskSnapshotOfStoppedInstance(t *testing.T) {
	h := newHarness(t)

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "cold")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	if snapshot.Kind != types.SnapshotKindDisk || snapshot.HypervisorVersion != "" || snapshot.IP != "" {
		t.Errorf("snapshot = %+v, want a disk snapshot with no hypervisor or address", snapshot)
	}
	if h.hv.paused != 0 || len(h.hv.snapshotDirs) != 0 {
		t.Error("a stopped instance's disk snapshot asked a hypervisor for something")
	}
	assertSameFile(t, h.manager.snapshotOverlayDiskPath(snapshot), h.overlay)
}

func TestCreateSnapshotGeneratesNameFromInstance(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if !strings.HasPrefix(snapshot.Name, h.instance.Name+"-") {
		t.Errorf("generated name %q does not start with the instance's", snapshot.Name)
	}
	if _, err := h.manager.Snapshot(snapshot.Name); err != nil {
		t.Errorf("generated name %q cannot be read back: %v", snapshot.Name, err)
	}
}

func TestCreateSnapshotRejections(t *testing.T) {
	t.Run("instance stopping", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)
		forceState(t, h.manager, h.instance.ID, types.InstanceStateStopping)

		_, err := h.manager.CreateSnapshot(t.Context(), h.instance, "nope")
		if !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("CreateSnapshot of a stopping instance = %v, want ErrInvalidState", err)
		}
	})

	t.Run("instance never started", func(t *testing.T) {
		h := newHarness(t)
		if err := os.Remove(h.overlay); err != nil {
			t.Fatal(err)
		}

		_, err := h.manager.CreateSnapshot(t.Context(), h.instance, "nope")
		if !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("CreateSnapshot of an instance with no disk = %v, want ErrInvalidState", err)
		}
	})

	// A volume is not in the snapshot, and the guest's memory of it would
	// not match what it holds by the time the snapshot is restored.
	t.Run("running instance writing to a volume", func(t *testing.T) {
		h := newHarness(t)
		h.instance.Mounts = []types.Mount{{Type: types.MountTypeVolume, Source: "data", Target: "/data"}}
		h.running(t)

		_, err := h.manager.CreateSnapshot(t.Context(), h.instance, "nope")
		if !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("CreateSnapshot = %v, want ErrInvalidState", err)
		}
		if h.hv.paused != 0 {
			t.Error("the guest was paused for a snapshot that was refused")
		}
	})

	// Snapshot names are the host's, not an instance's.
	t.Run("name another instance's snapshot has", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)
		other := seedInstance(t, h.definitions, "other")
		if err := os.MkdirAll(filepath.Dir(h.manager.overlayDiskPath(other)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.manager.overlayDiskPath(other), []byte("disk"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := h.manager.CreateSnapshot(t.Context(), other, "twice"); err != nil {
			t.Fatal(err)
		}
		_, err := h.manager.CreateSnapshot(t.Context(), h.instance, "twice")
		if !errors.Is(err, errdefs.ErrExists) {
			t.Errorf("second CreateSnapshot = %v, want ErrExists", err)
		}
	})

	t.Run("invalid name", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)

		_, err := h.manager.CreateSnapshot(t.Context(), h.instance, "../escape")
		if !errors.Is(err, errdefs.ErrInvalidArgument) {
			t.Errorf("CreateSnapshot(../escape) = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("hypervisor without snapshots", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)
		h.hv.capabilities = hypervisor.Capabilities{SupportsPause: true}

		_, err := h.manager.CreateSnapshot(t.Context(), h.instance, "nope")
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("CreateSnapshot = %v, want ErrUnsupported", err)
		}
	})
}

// TestCreateSnapshotCleansUpAfterFailure checks that a failed snapshot
// leaves nothing behind to be mistaken for a usable one, or to take space.
func TestCreateSnapshotCleansUpAfterFailure(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.hv.snapshotErr = errors.New("out of disk")

	if _, err := h.manager.CreateSnapshot(t.Context(), h.instance, "doomed"); err == nil {
		t.Fatal("CreateSnapshot succeeded despite the hypervisor failing")
	}

	if _, err := h.manager.Snapshot("doomed"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("Snapshot after a failure = %v, want ErrNotFound", err)
	}
	entries, err := os.ReadDir(filepath.Dir(h.definitions.SnapshotDir("doomed")))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed snapshot left %d entries behind", len(entries))
	}
	// The guest was paused for the attempt and must not be left that way.
	if h.hv.resumed != 1 {
		t.Errorf("resumed %d times, want 1", h.hv.resumed)
	}
}

func TestListAndDeleteSnapshots(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	for _, name := range []string{"second", "first"} {
		if _, err := h.manager.CreateSnapshot(t.Context(), h.instance, name); err != nil {
			t.Fatalf("CreateSnapshot(%s): %v", name, err)
		}
		time.Sleep(time.Millisecond)
	}

	// Oldest first, so a listing reads as a history.
	snapshots := h.manager.Snapshots()
	if len(snapshots) != 2 || snapshots[0].Name != "second" {
		t.Fatalf("snapshots = %+v, want second, then first", snapshots)
	}

	if err := h.manager.DeleteSnapshot(t.Context(), snapshots[1]); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if _, err := h.manager.Snapshot("first"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("Snapshot after delete = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(h.manager.snapshotDir(snapshots[1])); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the deleted snapshot's files are still there")
	}
	if err := h.manager.DeleteSnapshot(t.Context(), snapshots[1]); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("second DeleteSnapshot = %v, want ErrNotFound", err)
	}
}

// TestSnapshotOutlivesItsInstance checks that deleting an instance keeps its
// snapshots, and that restoring one then says why it cannot.
func TestSnapshotOutlivesItsInstance(t *testing.T) {
	h := newHarness(t)

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "keep")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.manager.Delete(t.Context(), h.instance, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := h.manager.Snapshot("keep"); err != nil {
		t.Errorf("Snapshot after its instance was deleted: %v", err)
	}
	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("RestoreSnapshot = %v, want ErrNotFound for the deleted instance", err)
	}
}

func TestRestoreMemorySnapshot(t *testing.T) {
	h := newHarness(t)
	h.running(t)

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "good")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	h.stopped(t)
	h.hv.resumed = 0

	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	if len(h.starter.restoredFrom) != 1 || h.starter.restoredFrom[0] != h.manager.snapshotDir(snapshot) {
		t.Errorf("restored from %v, want the snapshot's directory", h.starter.restoredFrom)
	}

	// A hypervisor restores a guest paused; the instance is only running
	// once it has been resumed.
	if h.hv.resumed != 1 {
		t.Errorf("resumed %d times, want 1", h.hv.resumed)
	}
	status := h.status(t)
	if status.State != types.InstanceStateRunning {
		t.Errorf("state = %s, want %s", status.State, types.InstanceStateRunning)
	}
	if status.HypervisorVersion != testHypervisorVersion {
		t.Errorf("hypervisor version = %q, want the snapshot's", status.HypervisorVersion)
	}

	// The guest's memory expects the disk as it was, so the disk written
	// after the snapshot must be gone, and no copy of it kept.
	assertSameFile(t, h.overlay, h.manager.snapshotOverlayDiskPath(snapshot))
	if _, err := os.Stat(h.manager.keptOverlayDiskPath(h.instance)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the disk the instance had is still kept after a successful restore")
	}
}

func TestRestoreDiskSnapshot(t *testing.T) {
	h := newHarness(t)

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "cold")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if err := os.WriteFile(h.overlay, []byte("written after the snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	assertSameFile(t, h.overlay, h.manager.snapshotOverlayDiskPath(snapshot))
	if h.starter.vmmCount() != 0 {
		t.Error("restoring a disk snapshot started a VMM")
	}
	if status := h.status(t); status.State != types.InstanceStateStopped {
		t.Errorf("state = %s, want the instance left %s", status.State, types.InstanceStateStopped)
	}
}

// Restoring is a start by a user: an unless-stopped instance a user stopped
// and then restored is no longer one a user stopped.
func TestRestoreSnapshotIsAUserStart(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "good")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	if instance, _ := h.definitions.Instance(h.instance.ID); instance.StoppedByUser {
		t.Error("the restored instance is still recorded as stopped by a user")
	}
}

// TestRestoredVMMFindsTheInstancesFiles checks that a restored VMM, which
// opens the files its snapshot names relative to its runtime directory,
// finds there the files of the instance as it is now: after a rename, its
// directory has moved.
func TestRestoredVMMFindsTheInstancesFiles(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
	if err != nil {
		t.Fatal(err)
	}
	h.stopped(t)

	renamed, err := h.manager.Rename(t.Context(), h.instance, "renamed")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	runtimeDir := h.manager.runtimeDir(h.instance.ID)
	for name, want := range map[string]string{
		h.starter.restoredSpec.Console.Path: h.manager.serialLogPath(renamed),
		overlayDiskFile:                     h.manager.overlayDiskPath(renamed),
	} {
		if filepath.IsAbs(name) {
			t.Errorf("the VMM is given %s, which names a directory", name)
			continue
		}
		if got, err := os.Readlink(filepath.Join(runtimeDir, name)); err != nil || got != want {
			t.Errorf("%s in the runtime directory leads to %q (%v), want %q", name, got, err, want)
		}
	}
}

func TestRestoreMemorySnapshotRefusesAChangedInstance(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, h *harness)
	}{
		{"mounts", func(t *testing.T, h *harness) {
			h.instance.Mounts = []types.Mount{{Type: types.MountTypeTmpfs, Target: "/scratch"}}
			h.definitions.instances[h.instance.Name] = h.instance
		}},
		{"address", func(t *testing.T, h *harness) {
			if err := h.manager.networks.Release(h.instance.NetworkName, h.instance.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.running(t)
			snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
			if err != nil {
				t.Fatal(err)
			}
			h.stopped(t)
			tt.change(t, h)

			_, err = h.manager.RestoreSnapshot(t.Context(), snapshot)
			if !errors.Is(err, errdefs.ErrInvalidState) {
				t.Errorf("RestoreSnapshot = %v, want ErrInvalidState", err)
			}
			if h.starter.vmmCount() != 0 {
				t.Error("a VMM was started for a refused restore")
			}
		})
	}
}

func TestRestoreSnapshotRejections(t *testing.T) {
	t.Run("running instance", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)
		snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
		if err != nil {
			t.Fatal(err)
		}

		_, err = h.manager.RestoreSnapshot(t.Context(), snapshot)
		if !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("RestoreSnapshot of a running instance = %v, want ErrInvalidState", err)
		}
	})

	// An instance on standby resumes its frozen guest on its overlay disk
	// at the next start, so a restore under it would hand that guest a disk
	// or memory it never had. It must be stopped first, which discards the
	// frozen guest.
	for _, kind := range []types.SnapshotKind{types.SnapshotKindMemory, types.SnapshotKindDisk} {
		t.Run("instance on standby, "+string(kind)+" snapshot", func(t *testing.T) {
			h := newHarness(t)
			// A disk snapshot is of a stopped instance, a memory one of a
			// running instance.
			if kind == types.SnapshotKindMemory {
				h.start(t)
			}
			snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Kind != kind {
				t.Fatalf("took a %s snapshot, want %s", snapshot.Kind, kind)
			}
			if kind == types.SnapshotKindDisk {
				h.start(t)
			}
			if err := h.manager.Standby(t.Context(), h.instance); err != nil {
				t.Fatal(err)
			}
			want := []byte("written before standby")
			if err := os.WriteFile(h.overlay, want, 0o600); err != nil {
				t.Fatal(err)
			}

			_, err = h.manager.RestoreSnapshot(t.Context(), snapshot)
			if !errors.Is(err, errdefs.ErrInvalidState) {
				t.Errorf("RestoreSnapshot of an instance on standby = %v, want ErrInvalidState", err)
			}
			if status := h.status(t); status.State != types.InstanceStateStandby {
				t.Errorf("state = %s, want the instance left %s", status.State, types.InstanceStateStandby)
			}
			if got, err := os.ReadFile(h.overlay); err != nil || !bytes.Equal(got, want) {
				t.Errorf("overlay disk changed (%d bytes, %v), want it left as it was", len(got), err)
			}
		})
	}

	// A snapshot can only be restored by the hypervisor version that took
	// it, so a daemon that does not ship that version must say so rather
	// than restore it with another.
	t.Run("hypervisor version gone", func(t *testing.T) {
		h := newHarness(t)
		h.running(t)
		snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "old")
		if err != nil {
			t.Fatal(err)
		}
		h.stopped(t)

		h.starter.version = "v50.0.0"

		_, err = h.manager.RestoreSnapshot(t.Context(), snapshot)
		if !errors.Is(err, errdefs.ErrInvalidState) {
			t.Errorf("RestoreSnapshot = %v, want a complaint about the missing version", err)
		}
		if status := h.status(t); status.State != types.InstanceStateStopped {
			t.Errorf("state = %s, want the instance left %s", status.State, types.InstanceStateStopped)
		}
	})
}

// TestFailedRestoreKeepsTheInstancesDisk checks that a restore that dies
// half-way leaves the instance Failed with the reason, not Starting, and
// with the disk it had rather than the snapshot's.
func TestFailedRestoreKeepsTheInstancesDisk(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "snap")
	if err != nil {
		t.Fatal(err)
	}
	h.stopped(t)
	want := []byte("written after the snapshot")
	if err := os.WriteFile(h.overlay, want, 0o600); err != nil {
		t.Fatal(err)
	}

	h.starter.restoreErr = errors.New("hypervisor refused")

	if _, err := h.manager.RestoreSnapshot(t.Context(), snapshot); err == nil {
		t.Fatal("RestoreSnapshot succeeded despite the hypervisor refusing")
	}

	status := h.status(t)
	if status.State != types.InstanceStateFailed || status.StateError == "" {
		t.Errorf("state = %s (%q), want %s with the reason", status.State, status.StateError, types.InstanceStateFailed)
	}
	if got, err := os.ReadFile(h.overlay); err != nil || !bytes.Equal(got, want) {
		t.Errorf("overlay disk = %q (%v), want the one the instance had", got, err)
	}
	if _, err := os.Stat(h.manager.keptOverlayDiskPath(h.instance)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the kept disk was left beside the one put back")
	}
}

// TestCreateSnapshotRecordsWhatTheGuestHas checks that a snapshot records
// the resources the guest runs with, which a resize can make differ from
// the definition.
func TestCreateSnapshotRecordsWhatTheGuestHas(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	if err := h.manager.transitionWith(h.instance, types.InstanceStateRunning, func(status *types.InstanceStatus) {
		status.VCPUs, status.MemoryBytes = 3, 3<<30
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := h.manager.CreateSnapshot(t.Context(), h.instance, "big")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if snapshot.VCPUs != 3 || snapshot.MemoryBytes != 3<<30 {
		t.Errorf("snapshot records %d vCPUs, %d bytes; want the guest's 3 vCPUs, %d bytes",
			snapshot.VCPUs, snapshot.MemoryBytes, int64(3<<30))
	}
}

// TestSnapshotGetsTimeForTheGuestsMemory checks that writing a snapshot is
// given time in proportion to the guest's memory, not the hypervisor API's
// usual bound, which a large guest would outlast.
func TestSnapshotGetsTimeForTheGuestsMemory(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	if err := h.manager.transitionWith(h.instance, types.InstanceStateRunning, func(status *types.InstanceStatus) {
		status.MemoryBytes = 64 << 30
	}); err != nil {
		t.Fatal(err)
	}

	var left time.Duration
	h.hv.onSnapshot = func(ctx context.Context) {
		deadline, _ := ctx.Deadline()
		left = time.Until(deadline)
	}
	if _, err := h.manager.CreateSnapshot(t.Context(), h.instance, "big"); err != nil {
		t.Fatal(err)
	}

	if left < 5*time.Minute {
		t.Errorf("a 64 GiB guest's snapshot was given %s", left)
	}
}

// stopped records the harness instance as stopped, its VMM gone, and its
// disk moved on from whatever was snapshotted.
func (h *harness) stopped(t *testing.T) {
	t.Helper()

	if err := h.manager.removeRuntimeDir(h.instance.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.overlay, []byte("written after the snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertSameFile fails the test unless the files at got and want hold the
// same bytes.
func assertSameFile(t *testing.T, got, want string) {
	t.Helper()

	gotData, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	wantData, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotData, wantData) {
		t.Errorf("%s is %d bytes, want the %d of %s", got, len(gotData), len(wantData), want)
	}
}

// TestSnapshotWaitsForMemoryWithTheGuestRunning checks that a guest whose
// memory is still being restored is snapshotted once it has been, and runs
// while it waits rather than staying paused.
func TestSnapshotWaitsForMemoryWithTheGuestRunning(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	h.hv.restoringSnapshots = 2

	if _, err := h.manager.CreateSnapshot(t.Context(), h.instance, "after-restore"); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	if h.hv.paused != 3 || h.hv.resumed != 3 {
		t.Errorf("paused %d times and resumed %d, want 3 and 3", h.hv.paused, h.hv.resumed)
	}
	if len(h.hv.snapshotDirs) != 1 {
		t.Errorf("snapshots taken = %d, want 1", len(h.hv.snapshotDirs))
	}
}
