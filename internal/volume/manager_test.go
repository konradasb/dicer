// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package volume

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(Config{DataDir: t.TempDir()})
	m.createDisk = func(_ context.Context, _ string, _ int64) error { return nil }
	return m
}

func TestCreateMakesAVolume(t *testing.T) {
	m := newTestManager(t)
	ctx := t.Context()

	volume, err := m.Create(ctx, "my-vol", 1024)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if volume.ID == "" {
		t.Error("ID should be non-empty")
	}
	if volume.Name != "my-vol" {
		t.Errorf("Name = %q, want %q", volume.Name, "my-vol")
	}
	if volume.SizeBytes != 1024 {
		t.Errorf("SizeBytes = %d, want 1024", volume.SizeBytes)
	}
	if volume.Path == "" {
		t.Error("Path should be non-empty")
	}
	if volume.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestCreateRejectsAZeroSize(t *testing.T) {
	m := newTestManager(t)
	ctx := t.Context()

	_, err := m.Create(ctx, "vol", 0)
	if err == nil {
		t.Fatal("Create() should return error for zero size")
	}
}

func TestCreateFailsWithItsDisk(t *testing.T) {
	m := newTestManager(t)
	ctx := t.Context()

	diskErr := errors.New("mkfs failed")
	m.createDisk = func(_ context.Context, _ string, _ int64) error { return diskErr }

	_, err := m.Create(ctx, "vol", 512)
	if err == nil {
		t.Fatal("Create() should propagate the disk creation error")
	}
	if !errors.Is(err, diskErr) {
		t.Errorf("error = %v, want to wrap %v", err, diskErr)
	}
}

func TestDeleteRemovesTheVolumeDirectory(t *testing.T) {
	m := newTestManager(t)
	ctx := t.Context()

	volume, err := m.Create(ctx, "to-delete", 512)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := m.Delete(volume.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := os.Stat(m.volumeDir(volume.ID)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("volume directory should be removed after Delete")
	}
}

func TestDeleteOfAMissingVolumeSucceeds(t *testing.T) {
	m := newTestManager(t)

	if err := m.Delete("ghost"); err != nil {
		t.Errorf("Delete() nonexistent error = %v, want nil", err)
	}
}

// TestDiskBytesCountsWhatASparseDiskTakesUp covers a volume's disk taking up
// only what has been written to it, not its size.
func TestDiskBytesCountsWhatASparseDiskTakesUp(t *testing.T) {
	m := NewManager(Config{DataDir: t.TempDir()})
	const size = 64 << 20
	m.createDisk = func(_ context.Context, path string, _ int64) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if err := f.Truncate(size); err != nil {
			return err
		}
		_, err = f.Write(make([]byte, 4096))
		return err
	}

	volume, err := m.Create(t.Context(), "data", size)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if got := m.DiskBytes(volume.ID); got <= 0 || got >= size {
		t.Errorf("DiskBytes() = %d, want more than 0 and less than the size, %d", got, size)
	}
	if got := m.DiskBytes("missing"); got != 0 {
		t.Errorf("DiskBytes() of a volume with no disk = %d, want 0", got)
	}
}
