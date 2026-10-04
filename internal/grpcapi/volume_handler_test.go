// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"os/exec"
	"strconv"
	"testing"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// TestVolumeDeletedIsRecorded checks a volume's deletion is recorded with its
// size, and a refused deletion is not.
func TestVolumeDeletedIsRecorded(t *testing.T) {
	s, definitions := newResourceServer(t)
	recorded := &fakeRecorder{}
	s.volumeHandler.events = recorded

	vol := types.Volume{ID: "v-1", Name: "data", SizeBytes: 10 << 30}
	if err := definitions.CreateVolume(vol); err != nil {
		t.Fatal(err)
	}
	if err := definitions.CreateInstance(types.InstanceSpec{
		ID: "i-1", Name: "db", Mounts: []types.Mount{{Type: types.MountVolume, Source: "data", Target: "/data"}},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteVolume(t.Context(), &dicerdv1.DeleteVolumeRequest{Name: "data"}); err == nil {
		t.Fatal("DeleteVolume of a mounted volume succeeded")
	}
	if len(recorded.events) != 0 {
		t.Fatalf("a refused deletion recorded %+v", recorded.events)
	}

	if err := definitions.DeleteInstance("db"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteVolume(t.Context(), &dicerdv1.DeleteVolumeRequest{Name: "data"}); err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}

	wantVolumeEvent(t, recorded.events, vol, events.ActionDeleted, "Deleted volume of 10 GiB and its data")
}

// TestVolumeCreatedIsRecorded checks a volume's creation is recorded with its
// size. Creating one formats it with mke2fs.
func TestVolumeCreatedIsRecorded(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs is not installed")
	}

	s, _ := newResourceServer(t)
	recorded := &fakeRecorder{}
	s.volumeHandler.events = recorded

	v, err := s.CreateVolume(t.Context(), &dicerdv1.CreateVolumeRequest{Name: "data", SizeBytes: 64 << 20})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	vol := types.Volume{ID: v.GetId(), Name: "data", SizeBytes: 64 << 20}
	wantVolumeEvent(t, recorded.events, vol, events.ActionCreated, "Created volume of 64 MiB, formatted ext4")
}

// wantVolumeEvent checks recorded is the one event about vol that action
// and message say.
func wantVolumeEvent(t *testing.T, recorded []events.Event, vol types.Volume, action events.Action, message string) {
	t.Helper()

	if len(recorded) != 1 {
		t.Fatalf("recorded %+v, want one event", recorded)
	}
	e := recorded[0]
	if e.Kind != events.KindVolume || e.ID != vol.ID || e.Name != vol.Name || e.Action != action {
		t.Errorf("event = %+v, want volume %s %s", e, vol.Name, action)
	}
	if e.Message != message {
		t.Errorf("message = %q, want %q", e.Message, message)
	}
	if want := strconv.FormatInt(vol.SizeBytes, 10); e.Attributes["size_bytes"] != want {
		t.Errorf("attributes = %v, want size_bytes %s", e.Attributes, want)
	}
}
