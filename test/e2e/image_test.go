// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// pruneImage is a second, small image, used where a test needs one it can
// delete without disturbing the image every other test boots.
const pruneImage = "docker.io/library/busybox:1.37"

// TestImagePullIsIdempotent pulls an image twice.
//
// Pulling converts the image to an EROFS disk, and the second pull has to
// recognise it already holds the result rather than fetching and converting it
// again. The registry round trip is real here, which is the part no unit test
// covers.
func TestImagePullIsIdempotent(t *testing.T) {
	t.Cleanup(func() { env.deleteImage(t, pruneImage) })

	first := env.dicer(t, "image", "pull", pruneImage)
	if !strings.Contains(first, "pulled") {
		t.Errorf("first pull said %q, expected it to report a pull", strings.TrimSpace(first))
	}

	// The daemon reports a cache hit and a fresh pull the same way, so the
	// evidence that nothing was refetched is the digest: a second conversion
	// would produce a new one.
	before := env.imageDigest(t, pruneImage)
	env.dicer(t, "image", "pull", pruneImage)

	if after := env.imageDigest(t, pruneImage); after != before {
		t.Errorf("digest changed across pulls: %q then %q", before, after)
	}
}

// TestImageInUseIsProtected checks that an image an instance is defined to
// boot cannot be deleted by accident.
func TestImageInUseIsProtected(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)

	if _, err := env.tryDicer(t, "image", "delete", testImage); err == nil {
		t.Error("deleting an image an instance is defined to use should be refused")
	}

	// The instance is the only thing holding it, so removing that releases
	// the image -- but nothing here deletes it, because every other test
	// boots from it.
	env.dicer(t, "instance", "delete", name, "--force")
}

// TestImagePruneRemovesUnusedImages checks that prune reclaims an image no
// instance refers to, and leaves the ones in use alone.
func TestImagePruneRemovesUnusedImages(t *testing.T) {
	name := instanceName(t)

	// An instance to hold testImage, so prune has something to spare as well
	// as something to remove.
	env.createInstance(t, name)
	env.dicer(t, "image", "pull", pruneImage)

	env.dicer(t, "image", "prune")

	if _, err := env.tryDicer(t, "image", "show", pruneImage); err == nil {
		t.Errorf("prune left the unused image %s behind", pruneImage)
	}
	if _, err := env.tryDicer(t, "image", "show", testImage); err != nil {
		t.Errorf("prune removed %s, which an instance is defined to boot: %v", testImage, err)
	}
}

// imageView is a row of `dicer image show --format json`.
type imageView struct {
	Name   string `json:"Name"`
	Digest string `json:"Digest"`
}

// imageDigest reads an image's manifest digest.
func (e *environment) imageDigest(t *testing.T, ref string) string {
	t.Helper()

	out := e.dicer(t, "image", "show", ref, "--format", "json")

	views := rows[imageView](t, out, "image "+ref)
	if len(views) != 1 {
		t.Fatalf("image show %s returned %d rows, want 1:\n%s", ref, len(views), out)
	}

	return views[0].Digest
}

// deleteImage removes an image, logging rather than failing so that it can be
// used for cleanup.
func (e *environment) deleteImage(t *testing.T, ref string) {
	t.Helper()

	ctx, cancel := cleanupContext()
	defer cancel()

	if _, err := e.runDicer(ctx, "image", "delete", ref); err != nil && !isNotFound(err) {
		t.Logf("cleanup: delete image %s: %v", ref, err)
	}
}
