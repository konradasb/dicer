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
			name:      "rootfsDir",
			digestHex: "abc123",
			fn:        m.rootfsDir,
			want:      "/data/tmp/abc123/rootfs",
		},
		{
			name:      "metadataPath",
			digestHex: "abc123",
			fn:        m.metadataPath,
			want:      "/data/images/abc123/metadata.json",
		},
		{
			name:      "unpackDir",
			digestHex: "abc123",
			fn:        m.unpackDir,
			want:      "/data/tmp/abc123",
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

func TestCreateDirs(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	if err := m.createDirs(); err != nil {
		t.Fatalf("createDirs failed: %v", err)
	}

	// Check all directories created
	dirs := []string{
		testDir,
		filepath.Join(testDir, "images"),
		filepath.Join(testDir, "tmp"),
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

func TestCreateDirsFails(t *testing.T) {
	// Beneath /dev/null, where nothing can be created.
	m := newTestManager("/dev/null/cannot/create")

	err := m.createDirs()
	if err == nil {
		t.Error("createDirs should fail with an invalid path")
	}
}

func TestEnsureImageDir(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	if err := m.ensureImageDir("abc123"); err != nil {
		t.Fatalf("ensureImageDir failed: %v", err)
	}

	dir := filepath.Join(testDir, "images", "abc123")
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("image dir not created: %v", err)
	}
}

func TestEnsureRootfsDir(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	if err := m.ensureRootfsDir("abc123"); err != nil {
		t.Fatalf("ensureRootfsDir failed: %v", err)
	}

	dir := filepath.Join(testDir, "tmp", "abc123", "rootfs")
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("rootfs directory not created: %v", err)
	}
}

func TestDiskExists(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

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

func TestStoredDigestHexes(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	// Empty initially
	dirs, err := m.storedDigestHexes()
	if err != nil {
		t.Fatalf("storedDigestHexes failed: %v", err)
	}
	if len(dirs) != 0 {
		t.Errorf("storedDigestHexes() length = %d, want 0", len(dirs))
	}

	// Create some image directories
	digests := []string{"abc123", "def456", "ghi789"}
	imagesDir := filepath.Join(testDir, "images")
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

	dirs, err = m.storedDigestHexes()
	if err != nil {
		t.Fatalf("storedDigestHexes failed: %v", err)
	}

	if len(dirs) != 3 {
		t.Errorf("storedDigestHexes() length = %d, want 3", len(dirs))
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

func TestStoredDigestHexesNotExist(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	// Don't create images dir

	dirs, err := m.storedDigestHexes()
	if err != nil {
		t.Fatalf("storedDigestHexes should not error when dir doesn't exist: %v", err)
	}
	if dirs != nil {
		t.Errorf("storedDigestHexes() should return nil for non-existent dir")
	}
}

func TestRemoveUnpackDir(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	// Create the rootfs directory
	if err := m.ensureRootfsDir("abc123"); err != nil {
		t.Fatalf("ensureRootfsDir failed: %v", err)
	}

	unpackDir := filepath.Join(testDir, "tmp", "abc123")
	if _, err := os.Stat(unpackDir); err != nil {
		t.Fatalf("rootfs directory not created: %v", err)
	}

	// Cleanup
	if err := m.removeUnpackDir("abc123"); err != nil {
		t.Fatalf("removeUnpackDir failed: %v", err)
	}

	// Should not exist anymore
	if _, err := os.Stat(unpackDir); !errors.Is(err, fs.ErrNotExist) {
		t.Error("unpack directory still exists after removal")
	}
}

func TestRemoveUnpackDirNotExist(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	// Cleanup non-existent should not error
	if err := m.removeUnpackDir("nonexistent"); err != nil {
		t.Errorf("removeUnpackDir should not error for non-existent: %v", err)
	}
}

func TestDeleteFiles(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	// Create image directory and disk
	if err := m.ensureImageDir("abc123"); err != nil {
		t.Fatalf("ensureImageDir failed: %v", err)
	}
	diskPath := m.diskPath("abc123")
	if err := os.WriteFile(diskPath, []byte("test"), 0o644); err != nil {
		t.Fatalf("create disk file failed: %v", err)
	}

	// Create the rootfs directory
	if err := m.ensureRootfsDir("abc123"); err != nil {
		t.Fatalf("ensureRootfsDir failed: %v", err)
	}

	// Delete
	if err := m.deleteFiles("abc123"); err != nil {
		t.Fatalf("deleteFiles failed: %v", err)
	}

	// The image directory should not exist
	imageDir := m.imageDir("abc123")
	if _, err := os.Stat(imageDir); !errors.Is(err, fs.ErrNotExist) {
		t.Error("image dir still exists after delete")
	}

	// The unpack directory should not exist
	unpackDir := filepath.Join(testDir, "tmp", "abc123")
	if _, err := os.Stat(unpackDir); !errors.Is(err, fs.ErrNotExist) {
		t.Error("unpack directory still exists after delete")
	}
}

func TestDeleteFilesNotExist(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	// Delete non-existent should not error
	if err := m.deleteFiles("nonexistent"); err != nil {
		t.Errorf("deleteFiles should not error for non-existent: %v", err)
	}
}

func TestSaveAndLoadMetadata(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	digestHex := "abc123"
	if err := m.ensureImageDir(digestHex); err != nil {
		t.Fatalf("ensureImageDir failed: %v", err)
	}

	now := time.Now()
	image := &types.Image{
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

	if err := m.saveMetadata(digestHex, image); err != nil {
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

	if loaded.Name != image.Name {
		t.Errorf("Name = %v, want %v", loaded.Name, image.Name)
	}
	if loaded.Digest != image.Digest {
		t.Errorf("Digest = %v, want %v", loaded.Digest, image.Digest)
	}
	if loaded.DiskPath != image.DiskPath {
		t.Errorf("DiskPath = %v, want %v", loaded.DiskPath, image.DiskPath)
	}
	if loaded.SizeBytes != image.SizeBytes {
		t.Errorf("SizeBytes = %v, want %v", loaded.SizeBytes, image.SizeBytes)
	}
	if len(loaded.Entrypoint) != len(image.Entrypoint) {
		t.Errorf("Entrypoint length = %d, want %d", len(loaded.Entrypoint), len(image.Entrypoint))
	}
	if len(loaded.Cmd) != len(image.Cmd) {
		t.Errorf("Cmd length = %d, want %d", len(loaded.Cmd), len(image.Cmd))
	}
	if len(loaded.Env) != len(image.Env) {
		t.Errorf("Env length = %d, want %d", len(loaded.Env), len(image.Env))
	}
	if loaded.WorkingDir != image.WorkingDir {
		t.Errorf("WorkingDir = %v, want %v", loaded.WorkingDir, image.WorkingDir)
	}
	if loaded.CreatedAt.Unix() != image.CreatedAt.Unix() {
		t.Errorf("CreatedAt = %v, want %v", loaded.CreatedAt, image.CreatedAt)
	}
	if loaded.UpdatedAt.Unix() != image.UpdatedAt.Unix() {
		t.Errorf("UpdatedAt = %v, want %v", loaded.UpdatedAt, image.UpdatedAt)
	}
}

func TestLoadMetadataNotExist(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

	_, err := m.loadMetadata("nonexistent")
	if err == nil {
		t.Error("loadMetadata should fail for non-existent digest")
	}
}

func TestLoadMetadataInvalidJSON(t *testing.T) {
	testDir := t.TempDir()
	m := newTestManager(testDir)

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
