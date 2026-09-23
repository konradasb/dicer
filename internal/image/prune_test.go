// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"

	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/registry"
	"github.com/konradasb/dicer/internal/types"
)

// pullTestImage pulls one image under a digest of the test's choosing, so a
// test can hold several at once.
func pullTestImage(t *testing.T, m *Manager, mock *mockRegistryClient, ref, digest string) *types.Image {
	t.Helper()

	mock.resolveFunc = func(context.Context, *reference.Ref) (string, error) { return digest, nil }

	img, err := m.Pull(t.Context(), ref, nil)
	if err != nil {
		t.Fatalf("Pull(%s): %v", ref, err)
	}

	return img
}

func newPruneTestManager(t *testing.T) (*Manager, *mockRegistryClient) {
	t.Helper()

	mock := &mockRegistryClient{}
	m, err := NewManager(Config{DataDir: t.TempDir(), Logger: discardLogger, Registry: mock})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	m.registry = mock
	m.packer = &mockPacker{}

	return m, mock
}

func TestPrune(t *testing.T) {
	m, mock := newPruneTestManager(t)

	const (
		keptDigest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		prunedDigest  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
		prunedDigest2 = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	)

	kept := pullTestImage(t, m, mock, "alpine:3.21", keptDigest)
	pruned := pullTestImage(t, m, mock, "alpine:3.20", prunedDigest)
	pullTestImage(t, m, mock, "debian:13", prunedDigest2)

	result, err := m.Prune(map[string]struct{}{keptDigest: {}})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(result.Images) != 2 {
		t.Fatalf("pruned %d images, want 2", len(result.Images))
	}
	if result.ReclaimedBytes == 0 {
		t.Error("prune reclaimed nothing")
	}

	// The image in use survives, files and all.
	if _, err := m.Get("alpine:3.21"); err != nil {
		t.Errorf("the image in use was pruned: %v", err)
	}
	if _, err := os.Stat(kept.DiskPath); err != nil {
		t.Errorf("the kept image lost its disk: %v", err)
	}

	// The others are gone, files and all.
	if _, err := m.Get("alpine:3.20"); err == nil {
		t.Error("an unused image survived the prune")
	}
	if _, err := os.Stat(pruned.DiskPath); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a pruned image left its disk behind")
	}

	// The layer cache is told to keep exactly what is left.
	if want := []string{digestHex(keptDigest)}; !slices.Equal(mock.prunedKeep, want) {
		t.Errorf("layer cache asked to keep %v, want %v", mock.prunedKeep, want)
	}
}

func TestPruneKeepsEverythingInUse(t *testing.T) {
	m, mock := newPruneTestManager(t)

	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	pullTestImage(t, m, mock, "alpine:3.21", digest)

	result, err := m.Prune(map[string]struct{}{digest: {}})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(result.Images) != 0 {
		t.Errorf("pruned %d images, want none", len(result.Images))
	}
	if len(m.List()) != 1 {
		t.Error("the image in use is gone")
	}
}

// TestPruneCountsCacheReclaim checks that what the layer cache gives back is
// part of what a prune reports reclaiming.
func TestPruneCountsCacheReclaim(t *testing.T) {
	m, mock := newPruneTestManager(t)
	mock.pruneReclaims = 4096

	result, err := m.Prune(map[string]struct{}{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if result.ReclaimedBytes != 4096 {
		t.Errorf("reclaimed %d, want the cache's 4096", result.ReclaimedBytes)
	}
}

// TestPullReportsProgress covers what a caller watching a pull sees: the
// stages in order, and the registry's byte counts passed through.
func TestPullReportsProgress(t *testing.T) {
	m, mock := newPruneTestManager(t)
	mock.events = []registry.Event{
		{Phase: registry.PhaseDownloading, Total: 100},
		{Phase: registry.PhaseDownloading, Downloaded: 60, Total: 100},
		{Phase: registry.PhaseUnpacking},
	}

	var got []types.PullProgress
	if _, err := m.Pull(t.Context(), "alpine:3.21", func(p types.PullProgress) { got = append(got, p) }); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	var stages []types.PullStage
	for _, p := range got {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
	}

	want := []types.PullStage{types.StageResolving, types.StageDownloading, types.StageUnpacking, types.StageConverting}
	if !slices.Equal(stages, want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}

	var sawBytes bool
	for _, p := range got {
		if p.Stage == types.StageDownloading && p.DownloadedBytes == 60 && p.TotalBytes == 100 {
			sawBytes = true
		}
	}
	if !sawBytes {
		t.Errorf("progress = %+v, want the registry's byte counts passed through", got)
	}
}

// TestPullWithoutProgressFunc checks that a caller that does not care about
// progress -- the instance lifecycle, for one -- can pass nil.
func TestPullWithoutProgressFunc(t *testing.T) {
	m, mock := newPruneTestManager(t)
	mock.events = []registry.Event{{Phase: registry.PhaseDownloading, Total: 10}}

	if _, err := m.Pull(t.Context(), "alpine:3.21", nil); err != nil {
		t.Fatalf("Pull with no progress func: %v", err)
	}
}
