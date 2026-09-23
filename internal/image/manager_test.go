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

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/image/reference"
	"github.com/dicer-sh/dicer/internal/registry"
)

// discardLogger keeps expected failures out of the test output.
var discardLogger = slog.New(slog.DiscardHandler)

// mockRegistryClient mocks the registry client for testing.
type mockRegistryClient struct {
	resolveFunc       func(ctx context.Context, ref *reference.Ref) (string, error)
	pullAndExportFunc func(ctx context.Context, imageRef, digest, exportDir string) (*registry.PullResult, error)

	// events, when set, are reported by PullAndExport as a real pull would.
	events []registry.Event

	prunedKeep    []string
	pruneReclaims int64

	// cacheSize is what CacheSize reports.
	cacheSize int64
}

func (m *mockRegistryClient) Resolve(ctx context.Context, ref *reference.Ref) (string, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(ctx, ref)
	}
	return "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", nil
}

func (m *mockRegistryClient) CacheSize() (int64, error) { return m.cacheSize, nil }

func (m *mockRegistryClient) PruneCache(keep []string) (int64, error) {
	m.prunedKeep = keep
	return m.pruneReclaims, nil
}

func (m *mockRegistryClient) PullAndExport(
	ctx context.Context, imageRef, digest, exportDir string, onEvent registry.EventFunc,
) (*registry.PullResult, error) {
	for _, ev := range m.events {
		if onEvent != nil {
			onEvent(ev)
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

	return &registry.PullResult{
		Metadata: &registry.Metadata{
			Entrypoint: []string{"/bin/sh"},
			Cmd:        []string{},
			Env:        map[string]string{"PATH": "/usr/bin"},
			WorkingDir: "/",
		},
		Digest: digest,
	}, nil
}

// mockPacker mocks the filesystem packer for testing.
type mockPacker struct {
	packFunc func(ctx context.Context, dir, outputPath string) (int64, error)
}

func (m *mockPacker) Pack(ctx context.Context, dir, outputPath string) (int64, error) {
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
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:             discardLogger,
		DataDir:            tmpDir,
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
		filepath.Join(tmpDir, "images"),
		filepath.Join(tmpDir, "tmp"),
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("directory %s not created: %v", dir, err)
		}
	}
}

func TestNewManager_Defaults(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
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
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:             discardLogger,
		DataDir:            tmpDir,
		MaxConcurrentPulls: 1,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	// Replace registry client and converter with mocks
	manager.registry = &mockRegistryClient{}
	manager.packer = &mockPacker{}

	ctx := context.Background()

	// Get image (will pull since it doesn't exist)
	img, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}

	if img == nil {
		t.Fatal("Pull() returned nil")
	}

	if img.DiskPath == "" {
		t.Error("DiskPath not set")
	}

	if img.SizeBytes == 0 {
		t.Error("SizeBytes not set")
	}

	// Second call should return cached image
	img2, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("second Pull() error = %v", err)
	}

	if img2.Digest != img.Digest {
		t.Error("second Pull() should return same image")
	}
}

func TestManager_Pull_PullError(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	// Mock client that fails
	testErr := errors.New("registry unavailable")
	manager.registry = &mockRegistryClient{
		pullAndExportFunc: func(context.Context, string, string, string) (*registry.PullResult, error) {
			return nil, testErr
		},
	}
	manager.packer = &mockPacker{}

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
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	testErr := errors.New("conversion failed")
	manager.registry = &mockRegistryClient{}
	manager.packer = &mockPacker{
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
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
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
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &mockRegistryClient{}
	manager.packer = &mockPacker{}

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
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &mockRegistryClient{}
	manager.packer = &mockPacker{}

	ctx := context.Background()

	// Pull image
	img, err := manager.Pull(ctx, "alpine:latest", nil)
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
	if _, err := os.Stat(img.DiskPath); !errors.Is(err, fs.ErrNotExist) {
		t.Error("disk file should be removed")
	}
}

func TestManager_Delete_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if err := manager.Delete("alpine:latest"); !errors.Is(err, dicer.ErrNotFound) {
		t.Errorf("Delete() of an absent image = %v, want ErrNotFound", err)
	}
}

func TestManager_LoadExistingImages(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake existing image on disk
	digestHex := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	imageDir := filepath.Join(tmpDir, "images", digestHex)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}

	diskPath := filepath.Join(imageDir, "disk.img")
	if err := os.WriteFile(diskPath, []byte("fake disk"), 0o644); err != nil {
		t.Fatalf("create disk file: %v", err)
	}

	m := newTestManager(tmpDir)
	img := &dicer.Image{
		Name:      "docker.io/library/alpine:latest",
		Digest:    "sha256:" + digestHex,
		DiskPath:  diskPath,
		SizeBytes: 9,
	}

	if err := m.saveMetadata(digestHex, img); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	// Create manager (should load existing image)
	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
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

	if images[0].Digest != img.Digest {
		t.Errorf("loaded image digest = %v, want %v", images[0].Digest, img.Digest)
	}
}

func TestManager_ConcurrentPulls(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:             discardLogger,
		DataDir:            tmpDir,
		MaxConcurrentPulls: 2,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &mockRegistryClient{}
	manager.packer = &mockPacker{}

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
	tmpDir := t.TempDir()

	// Create a fake existing image on disk first
	digestHex := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	imageDir := filepath.Join(tmpDir, "images", digestHex)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}

	diskPath := filepath.Join(imageDir, "disk.img")
	if err := os.WriteFile(diskPath, []byte("existing disk"), 0o644); err != nil {
		t.Fatalf("create disk file: %v", err)
	}

	m := newTestManager(tmpDir)
	img := &dicer.Image{
		Name:      "docker.io/library/alpine:latest",
		Digest:    "sha256:" + digestHex,
		DiskPath:  diskPath,
		SizeBytes: 13,
	}

	if err := m.saveMetadata(digestHex, img); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	// Now create manager and mock that returns same digest
	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &mockRegistryClient{
		resolveFunc: func(ctx context.Context, ref *reference.Ref) (string, error) {
			return "sha256:" + digestHex, nil
		},
	}
	manager.packer = &mockPacker{}

	ctx := context.Background()

	// GetImage should load from disk instead of pulling
	loaded, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}

	if loaded.SizeBytes != 13 {
		t.Errorf("SizeBytes = %d, want 13 (from disk)", loaded.SizeBytes)
	}
}

func TestManager_Pull_CorruptMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	// Create corrupt metadata
	digestHex := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	imageDir := filepath.Join(tmpDir, "images", digestHex)
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
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &mockRegistryClient{
		resolveFunc: func(ctx context.Context, ref *reference.Ref) (string, error) {
			return "sha256:" + digestHex, nil
		},
	}
	manager.packer = &mockPacker{}

	ctx := context.Background()

	// Should re-pull since metadata is corrupt
	img, err := manager.Pull(ctx, "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() should succeed by re-pulling: %v", err)
	}
	if img.DiskPath == "" {
		t.Error("re-pulled image has no disk path")
	}
}

func TestManager_Pull_MissingDiskFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create metadata but no disk file
	digestHex := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	imageDir := filepath.Join(tmpDir, "images", digestHex)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}

	diskPath := filepath.Join(imageDir, "disk.img")
	m := newTestManager(tmpDir)
	img := &dicer.Image{
		Name:      "docker.io/library/alpine:latest",
		Digest:    "sha256:" + digestHex,
		DiskPath:  diskPath,
		SizeBytes: 100,
	}

	if err := m.saveMetadata(digestHex, img); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	// Don't create disk file

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &mockRegistryClient{
		resolveFunc: func(ctx context.Context, ref *reference.Ref) (string, error) {
			return "sha256:" + digestHex, nil
		},
	}
	manager.packer = &mockPacker{}

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
	tmpDir := t.TempDir()

	cfg := Config{
		Logger:  discardLogger,
		DataDir: tmpDir,
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.registry = &mockRegistryClient{}
	manager.packer = &mockPacker{}

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

func TestManager_GetDoesNotPull(t *testing.T) {
	manager, err := NewManager(Config{DataDir: t.TempDir(), Logger: discardLogger})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	pulled := false
	manager.registry = &mockRegistryClient{
		pullAndExportFunc: func(context.Context, string, string, string) (*registry.PullResult, error) {
			pulled = true
			return nil, errors.New("unexpected pull")
		},
	}

	if _, err := manager.Get("alpine:latest"); !errors.Is(err, dicer.ErrNotFound) {
		t.Errorf("Get() of an absent image = %v, want ErrNotFound", err)
	}
	if pulled {
		t.Error("Get() pulled the image; it must only consult local state")
	}
}

func TestManager_DeleteAfterTagMoved(t *testing.T) {
	manager, err := NewManager(Config{DataDir: t.TempDir(), Logger: discardLogger})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.packer = &mockPacker{}

	const before = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const after = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	digest := before
	manager.registry = &mockRegistryClient{
		resolveFunc: func(context.Context, *reference.Ref) (string, error) { return digest, nil },
	}

	img, err := manager.Pull(context.Background(), "alpine:latest", nil)
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}

	// The tag now points somewhere else upstream. Deleting it must still
	// remove the image this host pulled under it.
	digest = after
	if err := manager.Delete("alpine:latest"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok := manager.index.get(img.Digest); ok {
		t.Errorf("image %s still present after Delete()", img.Digest)
	}
}
