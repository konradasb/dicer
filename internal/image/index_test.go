// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"errors"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

func TestIndex_CreateAndGet(t *testing.T) {
	s := newIndex()

	pulled := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	img := &types.Image{
		Name:      "docker.io/library/alpine:latest",
		Digest:    "sha256:abc123",
		CreatedAt: pulled,
		UpdatedAt: pulled,
	}

	// Create image
	if err := s.create(img); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// The times are the image's own, from when it was pulled: an image
	// indexed again when the daemon starts is not pulled anew.
	if !img.CreatedAt.Equal(pulled) || !img.UpdatedAt.Equal(pulled) {
		t.Errorf("times = %v, %v; want the pull's, %v", img.CreatedAt, img.UpdatedAt, pulled)
	}

	// Get image
	got, ok := s.get("sha256:abc123")
	if !ok {
		t.Fatal("get failed: image not found")
	}

	if got.Name != img.Name {
		t.Errorf("Name = %v, want %v", got.Name, img.Name)
	}
	if got.Digest != img.Digest {
		t.Errorf("Digest = %v, want %v", got.Digest, img.Digest)
	}
}

func TestIndex_CreateDuplicate(t *testing.T) {
	s := newIndex()

	img := &types.Image{
		Name:   "test",
		Digest: "sha256:abc",
	}

	if err := s.create(img); err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	// Second create should fail
	if err := s.create(img); !errors.Is(err, errdefs.ErrExists) {
		t.Errorf("create duplicate = %v, want %v", err, errdefs.ErrExists)
	}
}

func TestIndex_Delete(t *testing.T) {
	s := newIndex()

	img := &types.Image{
		Name:   "test",
		Digest: "sha256:abc",
	}

	if err := s.create(img); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if err := s.delete("sha256:abc"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if _, ok := s.get("sha256:abc"); ok {
		t.Error("image still exists after delete")
	}
}

func TestIndex_DeleteNotFound(t *testing.T) {
	s := newIndex()

	if err := s.delete("sha256:notfound"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("delete non-existent = %v, want %v", err, errdefs.ErrNotFound)
	}
}

func TestIndex_List(t *testing.T) {
	s := newIndex()

	images := []*types.Image{
		{Name: "img1", Digest: "sha256:abc"},
		{Name: "img2", Digest: "sha256:def"},
		{Name: "img3", Digest: "sha256:ghi"},
	}

	for _, img := range images {
		if err := s.create(img); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	got := s.list()
	if len(got) != 3 {
		t.Fatalf("list length = %d, want 3", len(got))
	}

	// Verify all images present
	digests := make(map[string]bool)
	for _, img := range got {
		digests[img.Digest] = true
	}

	for _, img := range images {
		if !digests[img.Digest] {
			t.Errorf("image %s not in list", img.Digest)
		}
	}
}

func TestIndex_ListEmpty(t *testing.T) {
	s := newIndex()

	got := s.list()
	if len(got) != 0 {
		t.Errorf("list length = %d, want 0", len(got))
	}
}

// A tag that moved upstream and was pulled again names two images; the one
// pulled last is what the tag means on this host.
func TestFindByNameReturnsTheLatestPull(t *testing.T) {
	x := newIndex()
	now := time.Now()
	older := &types.Image{Name: "docker.io/library/alpine:3.21", Digest: "sha256:old", CreatedAt: now.Add(-time.Hour)}
	newer := &types.Image{Name: "docker.io/library/alpine:3.21", Digest: "sha256:new", CreatedAt: now}
	// Indexed newest first, as a restart may load them: the pull times
	// decide, not the order they are indexed in.
	for _, img := range []*types.Image{newer, older} {
		if err := x.create(img); err != nil {
			t.Fatal(err)
		}
	}

	got, ok := x.findByName("docker.io/library/alpine:3.21")
	if !ok || got.Digest != newer.Digest {
		t.Errorf("findByName = %v, want the latest pull %s", got, newer.Digest)
	}
}

// markUsed only moves forward, and replaces the image rather than changing
// the one readers hold.
func TestIndexMarkUsed(t *testing.T) {
	x := newIndex()
	then := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	held := &types.Image{Digest: "sha256:abc", LastUsedAt: then}
	if err := x.create(held); err != nil {
		t.Fatal(err)
	}

	later := then.Add(time.Hour)
	updated, ok := x.markUsed("sha256:abc", later)
	if !ok || !updated.LastUsedAt.Equal(later) {
		t.Fatalf("markUsed = %v, %v; want the image used at %v", updated, ok, later)
	}
	if !held.LastUsedAt.Equal(then) {
		t.Error("markUsed changed the image a reader held")
	}

	if _, ok := x.markUsed("sha256:abc", then); ok {
		t.Error("markUsed moved the last use back")
	}
	if _, ok := x.markUsed("sha256:missing", later); ok {
		t.Error("markUsed marked an image that is not held")
	}
}
