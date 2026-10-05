// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
)

func TestRename(t *testing.T) {
	h := newHarness(t)

	renamed, err := h.manager.Rename(t.Context(), h.instance, "web-2")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if renamed.Name != "web-2" {
		t.Errorf("name = %q, want web-2", renamed.Name)
	}
	// The ID is what everything else is derived from, so a rename that
	// changed it would orphan the instance's runtime directory, its TAP
	// device and its address.
	if renamed.ID != h.instance.ID {
		t.Errorf("ID = %q, want it unchanged (%q)", renamed.ID, h.instance.ID)
	}

	if _, err := h.definitions.Instance("web"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("the old name still resolves: %v", err)
	}
	stored, err := h.definitions.Instance("web-2")
	if err != nil {
		t.Fatalf("the new name does not resolve: %v", err)
	}
	if stored.Name != "web-2" {
		t.Errorf("stored name = %q, want web-2", stored.Name)
	}

	// Looking it up by ID finds it under its new name.
	byID, err := h.definitions.Instance(h.instance.ID)
	if err != nil || byID.Name != "web-2" {
		t.Errorf("by ID = %+v, %v; want the renamed instance", byID, err)
	}
}

func TestRenameRecordsAnEvent(t *testing.T) {
	h := newHarness(t)

	if _, err := h.manager.Rename(t.Context(), h.instance, "web-2"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	e, ok := h.events.last(events.ActionRenamed)
	if !ok {
		t.Fatalf("no renamed event recorded, got %v", h.events.actions())
	}
	if e.Name != "web-2" {
		t.Errorf("event name = %q, want the new name", e.Name)
	}
	if e.Attributes["previous_name"] != "web" {
		t.Errorf("event attributes = %v, want the previous name", e.Attributes)
	}
}

// A guest that exited non-zero leaves the instance Failed, which has stopped
// just as surely as one that exited cleanly.
func TestRenameAllowsAFailedInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	h.exit(t, 1)
	h.waitForState(t, types.InstanceStateFailed)

	if _, err := h.manager.Rename(t.Context(), h.instance, "web-2"); err != nil {
		t.Fatalf("Rename of a failed instance: %v", err)
	}
	if _, err := h.definitions.Instance("web-2"); err != nil {
		t.Errorf("the failed instance was not renamed: %v", err)
	}
}

func TestRenameRefusesARunningInstance(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	_, err := h.manager.Rename(t.Context(), h.instance, "web-2")
	if !errors.Is(err, errdefs.ErrInvalidState) {
		t.Errorf("Rename of a running instance = %v, want an invalid state", err)
	}

	if _, err := h.definitions.Instance("web"); err != nil {
		t.Errorf("the refused rename moved the instance anyway: %v", err)
	}
}

func TestRenameRefusesANameInUse(t *testing.T) {
	h := newHarness(t)
	seedInstance(t, h.definitions, "taken")

	if _, err := h.manager.Rename(t.Context(), h.instance, "taken"); !errors.Is(err, errdefs.ErrExists) {
		t.Errorf("Rename onto a taken name = %v, want an already-exists error", err)
	}
}

// Renaming an instance to what it is already called is not a change, and is
// not an error either.
func TestRenameToTheSameNameDoesNothing(t *testing.T) {
	h := newHarness(t)

	renamed, err := h.manager.Rename(t.Context(), h.instance, h.instance.Name)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Name != h.instance.Name {
		t.Errorf("name = %q, want it unchanged", renamed.Name)
	}
	if _, ok := h.events.last(events.ActionRenamed); ok {
		t.Error("a rename that changed nothing recorded an event")
	}
}
