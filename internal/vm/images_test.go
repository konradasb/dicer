// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"testing"

	"github.com/dicer-sh/dicer"
)

func TestImagesInUseKeepsWhatGuestsAndSnapshotsNeed(t *testing.T) {
	h := newHarness(t)
	h.running(t)
	if _, err := h.mgr.CreateSnapshot(t.Context(), h.inst, "kept"); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	inUse, err := h.mgr.ImagesInUse()
	if err != nil {
		t.Fatalf("ImagesInUse: %v", err)
	}
	if _, ok := inUse["sha256:aaaa"]; !ok {
		t.Errorf("in use = %v, want the running guest's image", inUse)
	}

	// Stopped, the guest no longer needs its image, but its snapshot does.
	if err := h.mgr.clearRuntime(h.inst.ID); err != nil {
		t.Fatal(err)
	}
	if inUse, err = h.mgr.ImagesInUse(); err != nil {
		t.Fatal(err)
	}
	if _, ok := inUse["sha256:aaaa"]; !ok {
		t.Errorf("in use = %v, want the snapshot's image kept", inUse)
	}
}

// A stopped instance keeps the image its reference resolves to here: it is
// what it boots from next. A reference to an image this host does not hold
// keeps nothing.
func TestImagesInUseKeepsWhatDefinitionsName(t *testing.T) {
	h := newHarness(t)
	images, ok := h.mgr.images.(*fakeImages)
	if !ok {
		t.Fatalf("images is %T", h.mgr.images)
	}

	inUse, err := h.mgr.ImagesInUse()
	if err != nil {
		t.Fatal(err)
	}
	if len(inUse) != 0 {
		t.Errorf("in use = %v, want nothing while the image is not held", inUse)
	}

	images.held = &dicer.Image{Name: h.inst.ImageRef, Digest: "sha256:bbbb"}
	if inUse, err = h.mgr.ImagesInUse(); err != nil {
		t.Fatal(err)
	}
	if _, ok := inUse["sha256:bbbb"]; !ok {
		t.Errorf("in use = %v, want the image the definition names", inUse)
	}
}
