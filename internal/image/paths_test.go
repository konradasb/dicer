// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// newTestManager returns a Manager that knows only where its files go,
// which is all the path helpers need.
func newTestManager(dataDir string) *Manager {
	return &Manager{dataDir: dataDir}
}

func TestPaths(t *testing.T) {
	dataDir := "/data"
	m := newTestManager(dataDir)

	tests := []struct {
		name      string
		digestHex string
		fn        func(string) string
		want      string
	}{
		{
			name:      "imageDir",
			digestHex: "abc123",
			fn:        m.imageDir,
			want:      "/data/images/abc123",
		},
		{
			name:      "diskPath",
			digestHex: "abc123",
			fn:        m.diskPath,
			want:      "/data/images/abc123/disk.img",
		},
		{
			name:      "tmpRootfsPath",
			digestHex: "abc123",
			fn:        m.tmpRootfsPath,
			want:      "/data/tmp/abc123/rootfs",
		},
		{
			name:      "metadataPath",
			digestHex: "abc123",
			fn:        m.metadataPath,
			want:      "/data/images/abc123/metadata.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn(tt.digestHex)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInitialize(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	if err := m.initialize(); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	// Check all directories created
	dirs := []string{
		tmpDir,
		filepath.Join(tmpDir, "images"),
		filepath.Join(tmpDir, "tmp"),
	}

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("directory %s not created: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}

func TestInitializeError(t *testing.T) {
	// Try to initialize in a path that will fail
	m := newTestManager("/dev/null/cannot/create")

	err := m.initialize()
	if err == nil {
		t.Error("initialize should fail with invalid path")
	}
}

func TestEnsureImageDir(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	if err := m.ensureImageDir("abc123"); err != nil {
		t.Fatalf("ensureImageDir failed: %v", err)
	}

	dir := filepath.Join(tmpDir, "images", "abc123")
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("image dir not created: %v", err)
	}
}

func TestEnsureTmpDir(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	if err := m.ensureTmpDir("abc123"); err != nil {
		t.Fatalf("ensureTmpDir failed: %v", err)
	}

	dir := filepath.Join(tmpDir, "tmp", "abc123", "rootfs")
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("tmp dir not created: %v", err)
	}
}

func TestDiskExists(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	// Should not exist initially
	if m.diskExists("abc123") {
		t.Error("diskExists() = true, want false for non-existent disk")
	}

	// Create disk file
	if err := m.ensureImageDir("abc123"); err != nil {
		t.Fatalf("ensureImageDir failed: %v", err)
	}
	diskPath := m.diskPath("abc123")
	if err := os.WriteFile(diskPath, []byte("test"), 0o644); err != nil {
		t.Fatalf("create disk file failed: %v", err)
	}

	// Should exist now
	if !m.diskExists("abc123") {
		t.Error("diskExists() = false, want true for existing disk")
	}
}

func TestListImageDirs(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	// Empty initially
	dirs, err := m.listImageDirs()
	if err != nil {
		t.Fatalf("listImageDirs failed: %v", err)
	}
	if len(dirs) != 0 {
		t.Errorf("listImageDirs() length = %d, want 0", len(dirs))
	}

	// Create some image directories
	digests := []string{"abc123", "def456", "ghi789"}
	imagesDir := filepath.Join(tmpDir, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatalf("create images dir failed: %v", err)
	}

	for _, digest := range digests {
		dir := filepath.Join(imagesDir, digest)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create dir failed: %v", err)
		}
	}

	// Also create a file (should be ignored)
	if err := os.WriteFile(filepath.Join(imagesDir, "notadir.txt"), []byte("test"), 0o644); err != nil {
		t.Fatalf("create file failed: %v", err)
	}

	dirs, err = m.listImageDirs()
	if err != nil {
		t.Fatalf("listImageDirs failed: %v", err)
	}

	if len(dirs) != 3 {
		t.Errorf("listImageDirs() length = %d, want 3", len(dirs))
	}

	// Check all digests present
	digestSet := make(map[string]bool)
	for _, d := range dirs {
		digestSet[d] = true
	}
	for _, digest := range digests {
		if !digestSet[digest] {
			t.Errorf("digest %s not in list", digest)
		}
	}
}

func TestListImageDirsNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	// Don't create images dir

	dirs, err := m.listImageDirs()
	if err != nil {
		t.Fatalf("listImageDirs should not error when dir doesn't exist: %v", err)
	}
	if dirs != nil {
		t.Errorf("listImageDirs() should return nil for non-existent dir")
	}
}

func TestCleanupTmpDir(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	// Create tmp directory
	if err := m.ensureTmpDir("abc123"); err != nil {
		t.Fatalf("ensureTmpDir failed: %v", err)
	}

	tmpPath := filepath.Join(tmpDir, "tmp", "abc123")
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("tmp dir not created: %v", err)
	}

	// Cleanup
	if err := m.cleanupTmpDir("abc123"); err != nil {
		t.Fatalf("cleanupTmpDir failed: %v", err)
	}

	// Should not exist anymore
	if _, err := os.Stat(tmpPath); !errors.Is(err, fs.ErrNotExist) {
		t.Error("tmp dir still exists after cleanup")
	}
}

func TestCleanupTmpDirNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	// Cleanup non-existent should not error
	if err := m.cleanupTmpDir("nonexistent"); err != nil {
		t.Errorf("cleanupTmpDir should not error for non-existent: %v", err)
	}
}

func TestDeleteImage(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	// Create image directory and disk
	if err := m.ensureImageDir("abc123"); err != nil {
		t.Fatalf("ensureImageDir failed: %v", err)
	}
	diskPath := m.diskPath("abc123")
	if err := os.WriteFile(diskPath, []byte("test"), 0o644); err != nil {
		t.Fatalf("create disk file failed: %v", err)
	}

	// Create tmp directory
	if err := m.ensureTmpDir("abc123"); err != nil {
		t.Fatalf("ensureTmpDir failed: %v", err)
	}

	// Delete
	if err := m.deleteImage("abc123"); err != nil {
		t.Fatalf("deleteImage failed: %v", err)
	}

	// types.Image dir should not exist
	imageDir := m.imageDir("abc123")
	if _, err := os.Stat(imageDir); !errors.Is(err, fs.ErrNotExist) {
		t.Error("image dir still exists after delete")
	}

	// Tmp dir should not exist
	tmpDir2 := filepath.Join(tmpDir, "tmp", "abc123")
	if _, err := os.Stat(tmpDir2); !errors.Is(err, fs.ErrNotExist) {
		t.Error("tmp dir still exists after delete")
	}
}

func TestDeleteImageNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	// Delete non-existent should not error
	if err := m.deleteImage("nonexistent"); err != nil {
		t.Errorf("deleteImage should not error for non-existent: %v", err)
	}
}

func TestSaveAndLoadMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	digestHex := "abc123"
	if err := m.ensureImageDir(digestHex); err != nil {
		t.Fatalf("ensureImageDir failed: %v", err)
	}

	now := time.Now()
	img := &types.Image{
		Name:       "docker.io/library/alpine:latest",
		Digest:     "sha256:abc123",
		DiskPath:   "/path/to/disk.img",
		SizeBytes:  1024000,
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-c", "echo hello"},
		Env: map[string]string{
			"PATH": "/usr/local/bin:/usr/bin",
			"HOME": "/root",
		},
		WorkingDir: "/app",
		CreatedAt:  now.Add(-1 * time.Hour),
		UpdatedAt:  now,
	}

	if err := m.saveMetadata(digestHex, img); err != nil {
		t.Fatalf("saveMetadata failed: %v", err)
	}

	// Verify file exists
	metaPath := m.metadataPath(digestHex)
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("metadata file not created: %v", err)
	}

	loaded, err := m.loadMetadata(digestHex)
	if err != nil {
		t.Fatalf("loadMetadata failed: %v", err)
	}

	if loaded.Name != img.Name {
		t.Errorf("Name = %v, want %v", loaded.Name, img.Name)
	}
	if loaded.Digest != img.Digest {
		t.Errorf("Digest = %v, want %v", loaded.Digest, img.Digest)
	}
	if loaded.DiskPath != img.DiskPath {
		t.Errorf("DiskPath = %v, want %v", loaded.DiskPath, img.DiskPath)
	}
	if loaded.SizeBytes != img.SizeBytes {
		t.Errorf("SizeBytes = %v, want %v", loaded.SizeBytes, img.SizeBytes)
	}
	if len(loaded.Entrypoint) != len(img.Entrypoint) {
		t.Errorf("Entrypoint length = %d, want %d", len(loaded.Entrypoint), len(img.Entrypoint))
	}
	if len(loaded.Cmd) != len(img.Cmd) {
		t.Errorf("Cmd length = %d, want %d", len(loaded.Cmd), len(img.Cmd))
	}
	if len(loaded.Env) != len(img.Env) {
		t.Errorf("Env length = %d, want %d", len(loaded.Env), len(img.Env))
	}
	if loaded.WorkingDir != img.WorkingDir {
		t.Errorf("WorkingDir = %v, want %v", loaded.WorkingDir, img.WorkingDir)
	}
	if loaded.CreatedAt.Unix() != img.CreatedAt.Unix() {
		t.Errorf("CreatedAt = %v, want %v", loaded.CreatedAt, img.CreatedAt)
	}
	if loaded.UpdatedAt.Unix() != img.UpdatedAt.Unix() {
		t.Errorf("UpdatedAt = %v, want %v", loaded.UpdatedAt, img.UpdatedAt)
	}
}

func TestLoadMetadataNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	_, err := m.loadMetadata("nonexistent")
	if err == nil {
		t.Error("loadMetadata should fail for non-existent digest")
	}
}

func TestLoadMetadataInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	m := newTestManager(tmpDir)

	digestHex := "abc123"
	if err := m.ensureImageDir(digestHex); err != nil {
		t.Fatalf("ensureImageDir failed: %v", err)
	}

	metaPath := m.metadataPath(digestHex)
	if err := os.WriteFile(metaPath, []byte("not valid json"), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	_, err := m.loadMetadata(digestHex)
	if err == nil {
		t.Error("loadMetadata should fail for invalid JSON")
	}
}
