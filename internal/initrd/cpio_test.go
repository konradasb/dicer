// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCPIOReturnsTheArchiveSize(t *testing.T) {
	testDir := t.TempDir()
	dir := filepath.Join(testDir, "rootfs")
	path := filepath.Join(testDir, "initrd")

	for name, content := range map[string]string{
		"bin/test":   "#!/bin/sh\necho test",
		"etc/config": "config=value",
	} {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	size, err := writeCPIO(context.Background(), dir, path)
	if err != nil {
		t.Fatalf("writeCPIO: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("archive not written: %v", err)
	}
	if size == 0 || size != info.Size() {
		t.Errorf("size = %d, want the archive's, %d", size, info.Size())
	}
}

// TestWriteCPIOOfAnEmptyDirectoryHasATrailer checks that an empty tree still
// makes an archive, holding only the trailer.
func TestWriteCPIOOfAnEmptyDirectoryHasATrailer(t *testing.T) {
	size, err := writeCPIO(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "initrd"))
	if err != nil {
		t.Fatalf("writeCPIO: %v", err)
	}
	if size == 0 {
		t.Error("size = 0, want the trailer's")
	}
}

func TestWriteCPIOFails(t *testing.T) {
	testDir := t.TempDir()
	dir := filepath.Join(testDir, "rootfs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A file cannot be created beneath a regular file, whoever the test runs
	// as -- unlike a path under /nonexistent, which root can create.
	file := filepath.Join(testDir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir, path string
	}{
		{"missing directory", filepath.Join(testDir, "nonexistent"), filepath.Join(testDir, "initrd")},
		{"unwritable path", dir, filepath.Join(file, "initrd")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := writeCPIO(context.Background(), tc.dir, tc.path); err == nil {
				t.Error("writeCPIO succeeded, want an error")
			}
		})
	}
}

func TestWriteCPIOStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := writeCPIO(ctx, t.TempDir(), filepath.Join(t.TempDir(), "initrd"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("writeCPIO = %v, want context.Canceled", err)
	}
}
