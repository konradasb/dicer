// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/registry"
	"github.com/konradasb/dicer/internal/types"
)

// discardLogger keeps expected failures out of the test output.
var discardLogger = slog.New(slog.DiscardHandler)

// fakeRegistryClient stands in for a registry, pulling a one-file image.
type fakeRegistryClient struct {
	resolveFunc       func(ctx context.Context, ref *reference.Ref) (string, error)
	pullAndExportFunc func(ctx context.Context, imageRef, digest, exportDir string) (*registry.Metadata, error)

	// progress, when set, is reported by PullAndExport as a real pull would.
	progress []registry.Progress

	prunedKeep    []string
	pruneReclaims int64

	// cacheSize is what CacheSize reports.
	cacheSize int64
}

func (m *fakeRegistryClient) Resolve(ctx context.Context, ref *reference.Ref) (string, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(ctx, ref)
	}
	return "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", nil
}

func (m *fakeRegistryClient) CacheSize() (int64, error) { return m.cacheSize, nil }

func (m *fakeRegistryClient) PruneCache(keep []string) (int64, error) {
	m.prunedKeep = keep
	return m.pruneReclaims, nil
}

func (m *fakeRegistryClient) PullAndExport(
	ctx context.Context, imageRef, digest, exportDir string, onProgress registry.ProgressFunc,
) (*registry.Metadata, error) {
	for _, p := range m.progress {
		if onProgress != nil {
			onProgress(p)
		}
	}

	if m.pullAndExportFunc != nil {
		return m.pullAndExportFunc(ctx, imageRef, digest, exportDir)
	}

	// Create a minimal directory for testing
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(exportDir, "test.txt"), []byte("test"), 0o644); err != nil {
		return nil, err
	}

	return &registry.Metadata{
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{},
		Env:        map[string]string{"PATH": "/usr/bin"},
		WorkingDir: "/",
	}, nil
}

// fakePacker stands in for mkfs.erofs, writing a 15-byte disk.
type fakePacker struct {
	packFunc func(ctx context.Context, dir, outputPath string) (int64, error)
}

func (m *fakePacker) Pack(ctx context.Context, dir, outputPath string) (int64, error) {
	if m.packFunc != nil {
		return m.packFunc(ctx, dir, outputPath)
	}

	// Create a fake disk image
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(outputPath, []byte("fake disk image"), 0o644); err != nil {
		return 0, err
	}

	return 15, nil
}

func TestNewManager(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:             discardLogger,
		DataDir:            testDir,
		MaxConcurrentPulls: 3,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if manager == nil {
		t.Fatal("NewManager() returned nil")
	}

	// Verify directories created
	dirs := []string{
		filepath.Join(testDir, "images"),
		filepath.Join(testDir, "tmp"),
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("directory %s not created: %v", dir, err)
		}
	}
}

func TestNewManager_Defaults(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if manager.logger == nil {
		t.Error("logger should be set to default")
	}
}

func TestManager_Pull(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:             discardLogger,
		DataDir:            testDir,
		MaxConcurrentPulls: 1,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	// Replace the registry client and packer with fakes
	manager.registry = &fakeRegistryClient{}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	// Get image (will pull since it doesn't exist)
	image, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}

	if image == nil {
		t.Fatal("Pull() returned nil")
	}

	if image.DiskPath == "" {
		t.Error("DiskPath not set")
	}

	if image.SizeBytes == 0 {
		t.Error("SizeBytes not set")
	}

	// Second call should return cached image
	img2, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("second Pull() error = %v", err)
	}

	if img2.Digest != image.Digest {
		t.Error("second Pull() should return same image")
	}
}

func TestManager_Pull_PullError(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	// A registry that fails
	testErr := errors.New("registry unavailable")
	manager.registry = &fakeRegistryClient{
		pullAndExportFunc: func(context.Context, string, string, string) (*registry.Metadata, error) {
			return nil, testErr
		},
	}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	_, err = manager.Pull(ctx, "alpine:latest", nil)
	if err == nil {
		t.Fatal("Pull() should fail when pull fails")
	}

	if !errors.Is(err, testErr) {
		t.Errorf("error should wrap pull error, got: %v", err)
	}
}

func TestManager_Pull_ConvertError(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	testErr := errors.New("conversion failed")
	manager.registry = &fakeRegistryClient{}
	manager.packer = &fakePacker{
		packFunc: func(ctx context.Context, dir, outputPath string) (int64, error) {
			return 0, testErr
		},
	}

	ctx := context.Background()

	_, err = manager.Pull(ctx, "alpine:latest", nil)
	if err == nil {
		t.Fatal("Pull() should fail when convert fails")
	}

	var convertErr *ConvertError
	if !errors.As(err, &convertErr) {
		t.Errorf("error should be ConvertError, got: %T", err)
	}
}

func TestManager_Pull_InvalidReference(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	ctx := context.Background()

	_, err = manager.Pull(ctx, "INVALID::**", nil)
	if err == nil {
		t.Error("Pull() should fail with invalid reference")
	}

	if !errors.Is(err, ErrInvalidReference) {
		t.Errorf("Pull() error = %v, want ErrInvalidReference", err)
	}
}

func TestManager_List(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &fakeRegistryClient{}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	// Initially empty
	images := manager.List()
	if len(images) != 0 {
		t.Errorf("initial List() length = %d, want 0", len(images))
	}

	// Pull an image
	_, err = manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull(alpine) error = %v", err)
	}

	// List should return it
	images = manager.List()

	if len(images) != 1 {
		t.Errorf("List() length = %d, want 1", len(images))
	}
}

func TestManager_Delete(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &fakeRegistryClient{}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	// Pull image
	image, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}

	// Delete image
	err = manager.Delete("alpine:latest")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify image removed from index
	images := manager.List()
	if len(images) != 0 {
		t.Error("image should be removed from index")
	}

	// Verify disk file removed
	if _, err := os.Stat(image.DiskPath); !errors.Is(err, fs.ErrNotExist) {
		t.Error("disk file should be removed")
	}
}

func TestManager_Delete_NotFound(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if err := manager.Delete("alpine:latest"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("Delete() of an absent image = %v, want ErrNotFound", err)
	}
}

func TestManager_LoadExistingImages(t *testing.T) {
	testDir := t.TempDir()

	// Create a fake existing image on disk
	digestHex := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	imageDir := filepath.Join(testDir, "images", digestHex)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}

	diskPath := filepath.Join(imageDir, "disk.img")
	if err := os.WriteFile(diskPath, []byte("fake disk"), 0o644); err != nil {
		t.Fatalf("create disk file: %v", err)
	}

	m := newTestManager(testDir)
	image := &types.Image{
		Name:      "docker.io/library/alpine:latest",
		Digest:    "sha256:" + digestHex,
		DiskPath:  diskPath,
		SizeBytes: 9,
	}

	if err := m.saveMetadata(digestHex, image); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	// Create manager (should load existing image)
	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	// Verify image was loaded
	images := manager.List()

	if len(images) != 1 {
		t.Fatalf("List() length = %d, want 1", len(images))
	}

	if images[0].Digest != image.Digest {
		t.Errorf("loaded image digest = %v, want %v", images[0].Digest, image.Digest)
	}
}

func TestManager_ConcurrentPulls(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:             discardLogger,
		DataDir:            testDir,
		MaxConcurrentPulls: 2,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &fakeRegistryClient{}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	// Pull same image concurrently - should deduplicate
	done := make(chan error, 3)

	for range 3 {
		go func() {
			_, err := manager.Pull(ctx, "alpine:latest", nil)
			done <- err
		}()
	}

	// All should succeed
	for i := range 3 {
		if err := <-done; err != nil {
			t.Errorf("concurrent Pull() %d error = %v", i, err)
		}
	}

	// Should only have one image (deduplicated)
	images := manager.List()
	if len(images) != 1 {
		t.Errorf("concurrent pulls should result in 1 image, got %d", len(images))
	}
}

func TestManager_Pull_LoadFromDisk(t *testing.T) {
	testDir := t.TempDir()

	// Create a fake existing image on disk first
	digestHex := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	imageDir := filepath.Join(testDir, "images", digestHex)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}

	diskPath := filepath.Join(imageDir, "disk.img")
	if err := os.WriteFile(diskPath, []byte("existing disk"), 0o644); err != nil {
		t.Fatalf("create disk file: %v", err)
	}

	m := newTestManager(testDir)
	image := &types.Image{
		Name:      "docker.io/library/alpine:latest",
		Digest:    "sha256:" + digestHex,
		DiskPath:  diskPath,
		SizeBytes: 13,
	}

	if err := m.saveMetadata(digestHex, image); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	// Now create manager and fakeRegistry that returns same digest
	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &fakeRegistryClient{
		resolveFunc: func(ctx context.Context, ref *reference.Ref) (string, error) {
			return "sha256:" + digestHex, nil
		},
	}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	// Pull should load from disk instead of pulling
	loaded, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}

	if loaded.SizeBytes != 13 {
		t.Errorf("SizeBytes = %d, want 13 (from disk)", loaded.SizeBytes)
	}
}

func TestManager_Pull_CorruptMetadata(t *testing.T) {
	testDir := t.TempDir()

	// Create corrupt metadata
	digestHex := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	imageDir := filepath.Join(testDir, "images", digestHex)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}

	diskPath := filepath.Join(imageDir, "disk.img")
	if err := os.WriteFile(diskPath, []byte("disk"), 0o644); err != nil {
		t.Fatalf("create disk file: %v", err)
	}

	// Write invalid JSON
	metaPath := filepath.Join(imageDir, "metadata.json")
	if err := os.WriteFile(metaPath, []byte("invalid json"), 0o644); err != nil {
		t.Fatalf("write corrupt metadata: %v", err)
	}

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &fakeRegistryClient{
		resolveFunc: func(ctx context.Context, ref *reference.Ref) (string, error) {
			return "sha256:" + digestHex, nil
		},
	}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	// Should re-pull since metadata is corrupt
	image, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() should succeed by re-pulling: %v", err)
	}
	if image.DiskPath == "" {
		t.Error("re-pulled image has no disk path")
	}
}

func TestManager_Pull_MissingDiskFile(t *testing.T) {
	testDir := t.TempDir()

	// Create metadata but no disk file
	digestHex := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	imageDir := filepath.Join(testDir, "images", digestHex)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}

	diskPath := filepath.Join(imageDir, "disk.img")
	m := newTestManager(testDir)
	image := &types.Image{
		Name:      "docker.io/library/alpine:latest",
		Digest:    "sha256:" + digestHex,
		DiskPath:  diskPath,
		SizeBytes: 100,
	}

	if err := m.saveMetadata(digestHex, image); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	// Don't create disk file

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &fakeRegistryClient{
		resolveFunc: func(ctx context.Context, ref *reference.Ref) (string, error) {
			return "sha256:" + digestHex, nil
		},
	}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	// Should re-pull since disk is missing
	loaded, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() should succeed by re-pulling: %v", err)
	}
	if loaded.DiskPath == "" {
		t.Error("re-pulled image has no disk path")
	}
}

func TestManager_Delete_PartialCleanup(t *testing.T) {
	testDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: testDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &fakeRegistryClient{}
	manager.packer = &fakePacker{}

	ctx := context.Background()

	// Pull image
	_, err = manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}

	// Manually delete disk file to simulate partial state
	images := manager.List()
	if len(images) > 0 {
		os.Remove(images[0].DiskPath)
	}

	// Delete should still succeed
	err = manager.Delete("alpine:latest")
	if err != nil {
		t.Fatalf("Delete() should succeed even with missing files: %v", err)
	}
}

func TestImageDoesNotPull(t *testing.T) {
	manager, err := NewManager(Config{DataDir: t.TempDir(), Logger: discardLogger})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	pulled := false
	manager.registry = &fakeRegistryClient{
		pullAndExportFunc: func(context.Context, string, string, string) (*registry.Metadata, error) {
			pulled = true
			return nil, errors.New("unexpected pull")
		},
	}

	if _, err := manager.Image("alpine:latest"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("Image() of an absent image = %v, want ErrNotFound", err)
	}
	if pulled {
		t.Error("Image() pulled the image; it must only consult local state")
	}
}

func TestManager_DeleteAfterTagMoved(t *testing.T) {
	manager, err := NewManager(Config{DataDir: t.TempDir(), Logger: discardLogger})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.packer = &fakePacker{}

	const before = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const after = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	digest := before
	manager.registry = &fakeRegistryClient{
		resolveFunc: func(context.Context, *reference.Ref) (string, error) { return digest, nil },
	}

	image, err := manager.Pull(context.Background(), "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}

	// The tag now points somewhere else upstream. Deleting it must still
	// remove the image this host pulled under it.
	digest = after
	if err := manager.Delete("alpine:latest"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok := manager.index.get(image.Digest); ok {
		t.Errorf("image %s still present after Delete()", image.Digest)
	}
}

// TestEnsureFollowsThePullPolicy checks when Ensure asks a registry, and that
// PullPolicyNever refuses an image the host does not hold rather than
// fetching it.
func TestEnsureFollowsThePullPolicy(t *testing.T) {
	tests := []struct {
		name         string
		policy       types.PullPolicy
		held         bool
		wantRegistry bool
		wantErr      error
	}{
		{name: "missing pulls an image the host lacks", policy: types.PullPolicyMissing, wantRegistry: true},
		{name: "missing uses the image held", policy: types.PullPolicyMissing, held: true},
		{name: "unset is missing", policy: "", held: true},
		{name: "always asks the registry for an image held", policy: types.PullPolicyAlways, held: true, wantRegistry: true},
		{name: "never uses the image held", policy: types.PullPolicyNever, held: true},
		{name: "never refuses an image the host lacks", policy: types.PullPolicyNever, wantErr: errdefs.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := NewManager(Config{DataDir: t.TempDir(), Logger: discardLogger})
			if err != nil {
				t.Fatalf("NewManager() error = %v", err)
			}
			manager.packer = &fakePacker{}

			asked := 0
			manager.registry = &fakeRegistryClient{
				resolveFunc: func(context.Context, *reference.Ref) (string, error) {
					asked++
					return "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", nil
				},
			}

			if tt.held {
				if _, err := manager.Pull(t.Context(), "alpine:latest", nil); err != nil {
					t.Fatalf("Pull() error = %v", err)
				}
				asked = 0
			}

			image, err := manager.Ensure(t.Context(), "alpine:latest", tt.policy)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Ensure() error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil || image == nil {
				t.Fatalf("Ensure() = %v, %v, want the image", image, err)
			}

			if got := asked > 0; got != tt.wantRegistry {
				t.Errorf("registry asked %d times, want asked = %t", asked, tt.wantRegistry)
			}
		})
	}
}

// TestEnsureMarksTheImageUsed checks that an image held is marked used, so
// that garbage collection spares it until the instance it is for is defined.
func TestEnsureMarksTheImageUsed(t *testing.T) {
	for _, policy := range []types.PullPolicy{types.PullPolicyMissing, types.PullPolicyAlways, types.PullPolicyNever} {
		t.Run(string(policy), func(t *testing.T) {
			manager, err := NewManager(Config{DataDir: t.TempDir(), Logger: discardLogger})
			if err != nil {
				t.Fatalf("NewManager() error = %v", err)
			}
			manager.registry = &fakeRegistryClient{}
			manager.packer = &fakePacker{}

			image, err := manager.Pull(t.Context(), "alpine:latest", nil)
			if err != nil {
				t.Fatalf("Pull() error = %v", err)
			}
			stale := time.Now().Add(-24 * time.Hour)
			manager.index.images[image.Digest].LastUsedAt = stale

			if _, err := manager.Ensure(t.Context(), "alpine:latest", policy); err != nil {
				t.Fatalf("Ensure() error = %v", err)
			}

			got, ok := manager.index.get(image.Digest)
			if !ok {
				t.Fatal("image gone after Ensure()")
			}
			if !got.LastUsedAt.After(stale) {
				t.Errorf("LastUsedAt = %v, want it moved on from %v", got.LastUsedAt, stale)
			}
		})
	}
}
