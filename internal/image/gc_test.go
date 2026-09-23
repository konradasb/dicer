// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"slices"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

var gcNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// usedAgo is an image last used d before gcNow.
func usedAgo(digest string, d time.Duration) *types.Image {
	return &types.Image{Name: "img-" + digest, Digest: digest, LastUsedAt: gcNow.Add(-d), SizeBytes: 100}
}

func digests(images []*types.Image) []string {
	out := make([]string, 0, len(images))
	for _, img := range images {
		out = append(out, img.Digest)
	}
	return out
}

func TestGCPolicyExpired(t *testing.T) {
	images := []*types.Image{
		usedAgo("old", 30*24*time.Hour),
		usedAgo("week", 8*24*time.Hour),
		usedAgo("fresh", time.Hour),
		usedAgo("old-but-in-use", 30*24*time.Hour),
	}
	inUse := map[string]struct{}{"old-but-in-use": {}}

	got := GCPolicy{MaxUnusedAge: 7 * 24 * time.Hour}.expired(images, inUse, gcNow)
	if want := []string{"old", "week"}; !slices.Equal(digests(got), want) {
		t.Errorf("expired = %v, want %v", digests(got), want)
	}

	if got := (GCPolicy{MaxSize: 1}).expired(images, inUse, gcNow); got != nil {
		t.Errorf("a policy with no age limit expired %v", digests(got))
	}
}

func TestCollectable(t *testing.T) {
	images := []*types.Image{
		usedAgo("b", 2*time.Hour),
		usedAgo("a", 3*time.Hour),
		usedAgo("in-use", 5*time.Hour),
		usedAgo("just-pulled", time.Minute),
		usedAgo("c", time.Hour),
	}
	inUse := map[string]struct{}{"in-use": {}}

	// Least recently used first; nothing in use, nor used within the grace
	// period.
	got := collectable(images, inUse, gcNow)
	if want := []string{"a", "b", "c"}; !slices.Equal(digests(got), want) {
		t.Errorf("collectable = %v, want %v", digests(got), want)
	}
}

func TestGCPolicyEnabled(t *testing.T) {
	for p, want := range map[GCPolicy]bool{
		{}:                        false,
		{MaxUnusedAge: time.Hour}: true,
		{MaxSize: 1 << 30}:        true,
	} {
		if p.Enabled() != want {
			t.Errorf("%+v.Enabled() = %v, want %v", p, !want, want)
		}
	}
}

// gcImages pulls one image per digest, each last used the given time before
// gcNow, into a fresh store.
func gcImages(t *testing.T, used map[string]time.Duration) (*Manager, *mockRegistryClient) {
	t.Helper()

	m, mock := newPruneTestManager(t)
	for digest, ago := range used {
		img := pullTestImage(t, m, mock, "docker.io/library/"+digest+":1", "sha256:"+digest)
		setLastUsed(t, m, img.Digest, gcNow.Add(-ago))
	}
	return m, mock
}

// setLastUsed records an image as last used at at, as if it had been.
func setLastUsed(t *testing.T, m *Manager, digest string, at time.Time) {
	t.Helper()

	m.index.mu.Lock()
	defer m.index.mu.Unlock()

	img, ok := m.index.images[digest]
	if !ok {
		t.Fatalf("no image %s", digest)
	}
	updated := *img
	updated.LastUsedAt = at
	m.index.images[digest] = &updated
}

func heldDigests(m *Manager) []string {
	held := digests(m.List())
	slices.Sort(held)
	return held
}

func TestCollectGarbageRemovesImagesUnusedTooLong(t *testing.T) {
	m, _ := gcImages(t, map[string]time.Duration{
		"stale": 10 * 24 * time.Hour, "kept": 10 * 24 * time.Hour, "recent": time.Hour,
	})

	result, err := m.CollectGarbage(GCPolicy{MaxUnusedAge: 7 * 24 * time.Hour},
		map[string]struct{}{"sha256:kept": {}}, gcNow)
	if err != nil {
		t.Fatalf("CollectGarbage: %v", err)
	}

	if len(result.Removed) != 1 || result.Removed[0].Image.Digest != "sha256:stale" ||
		result.Removed[0].Reason != GCReasonUnused {
		t.Errorf("removed %+v, want only the stale image, for being unused", result.Removed)
	}
	if got, want := heldDigests(m), []string{"sha256:kept", "sha256:recent"}; !slices.Equal(got, want) {
		t.Errorf("held = %v, want %v", got, want)
	}
}

// An image in use is recorded as used, on disk, so its age counts from when
// it stopped being used -- across restarts too.
func TestCollectGarbageRecordsUseOfImagesInUse(t *testing.T) {
	m, _ := gcImages(t, map[string]time.Duration{"busy": 30 * 24 * time.Hour})

	if _, err := m.CollectGarbage(GCPolicy{MaxUnusedAge: time.Hour},
		map[string]struct{}{"sha256:busy": {}}, gcNow); err != nil {
		t.Fatal(err)
	}

	img, err := m.loadMetadata(digestHex("sha256:busy"))
	if err != nil {
		t.Fatal(err)
	}
	if !img.LastUsedAt.Equal(gcNow) {
		t.Errorf("recorded last use = %v, want %v", img.LastUsedAt, gcNow)
	}
}

// Over the size limit, the least recently used go first, and only as many
// as it takes.
func TestCollectGarbageBringsTheStoreUnderMaxSize(t *testing.T) {
	m, mock := gcImages(t, map[string]time.Duration{
		"oldest": 3 * time.Hour, "older": 2 * time.Hour, "newest": time.Hour, "busy": 5 * time.Hour,
	})
	mock.cacheSize = 10
	// Each image's disk is 15 bytes: four of them and the cache are 70.

	result, err := m.CollectGarbage(GCPolicy{MaxSize: 45},
		map[string]struct{}{"sha256:busy": {}}, gcNow)
	if err != nil {
		t.Fatalf("CollectGarbage: %v", err)
	}

	var removed []string
	for _, r := range result.Removed {
		if r.Reason != GCReasonSize {
			t.Errorf("%s removed for %s, want size", r.Image.Digest, r.Reason)
		}
		removed = append(removed, r.Image.Digest)
	}
	if want := []string{"sha256:oldest", "sha256:older"}; !slices.Equal(removed, want) {
		t.Errorf("removed %v, want the two least recently used", removed)
	}
	if got, want := heldDigests(m), []string{"sha256:busy", "sha256:newest"}; !slices.Equal(got, want) {
		t.Errorf("held = %v, want %v", got, want)
	}
}

// Nothing in use goes, however far over its size the store is.
func TestCollectGarbageNeverRemovesImagesInUse(t *testing.T) {
	m, mock := gcImages(t, map[string]time.Duration{"a": 30 * 24 * time.Hour, "b": 30 * 24 * time.Hour})
	mock.cacheSize = 1 << 30

	result, err := m.CollectGarbage(GCPolicy{MaxUnusedAge: time.Hour, MaxSize: 1},
		map[string]struct{}{"sha256:a": {}, "sha256:b": {}}, gcNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 0 {
		t.Errorf("removed %+v, want nothing", result.Removed)
	}
}

// An image's times are its own: loading it again when the daemon starts
// neither makes it new nor forgets when it was last used, and an image
// recorded before its use was starts out used now rather than unused
// forever.
func TestLoadKeepsTheImagesTimes(t *testing.T) {
	m, mock := newPruneTestManager(t)
	img := pullTestImage(t, m, mock, "docker.io/library/alpine:3.21", "sha256:abc")

	pulled, used := gcNow.Add(-48*time.Hour), gcNow.Add(-24*time.Hour)
	img.CreatedAt, img.UpdatedAt, img.LastUsedAt = pulled, pulled, used
	if err := m.saveMetadata(digestHex(img.Digest), img); err != nil {
		t.Fatal(err)
	}

	legacy := pullTestImage(t, m, mock, "docker.io/library/nginx:1.27", "sha256:def")
	legacy.LastUsedAt = time.Time{}
	if err := m.saveMetadata(digestHex(legacy.Digest), legacy); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewManager(Config{DataDir: m.dataDir, Logger: discardLogger, Registry: mock})
	if err != nil {
		t.Fatal(err)
	}

	got, err := reloaded.Get("docker.io/library/alpine:3.21")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(pulled) || !got.LastUsedAt.Equal(used) {
		t.Errorf("reloaded times = created %v, used %v; want %v, %v", got.CreatedAt, got.LastUsedAt, pulled, used)
	}

	got, err = reloaded.Get("docker.io/library/nginx:1.27")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(got.LastUsedAt) > time.Minute {
		t.Errorf("an image with no recorded use was last used %v, want now", got.LastUsedAt)
	}
}
