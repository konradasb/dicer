// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package diskfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// needMke2fs skips a test on a host without mke2fs.
func needMke2fs(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs is not installed")
	}
}

// TestCreateExt4MakesASparseDisk checks the disk file is the size asked for,
// takes up far less than that, and is left alone in its directory.
func TestCreateExt4MakesASparseDisk(t *testing.T) {
	needMke2fs(t)

	const size = 64 << 20
	path := filepath.Join(t.TempDir(), "disks", "overlay.raw")
	if err := CreateExt4(t.Context(), path, size); err != nil {
		t.Fatalf("CreateExt4: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Errorf("size = %d, want %d", info.Size(), size)
	}
	if got := AllocatedBytes(path); got <= 0 || got >= size {
		t.Errorf("AllocatedBytes = %d, want more than 0 and less than the size, %d", got, size)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d files, want only the disk", len(entries))
	}
}

// TestCreateExt4FromCopiesTheDirectory checks the filesystem holds the files
// it was made from.
func TestCreateExt4FromCopiesTheDirectory(t *testing.T) {
	needMke2fs(t)
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs is not installed")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.raw")
	if err := CreateExt4From(t.Context(), path, 4<<20, dir); err != nil {
		t.Fatalf("CreateExt4From: %v", err)
	}

	out, err := exec.CommandContext(t.Context(), "debugfs", "-R", "cat /config.json", path).Output()
	if err != nil {
		t.Fatalf("read the disk: %v", err)
	}
	if !strings.Contains(string(out), `{"a":1}`) {
		t.Errorf("config.json on the disk = %q, want what was written", out)
	}
}

// TestFailedCreateLeavesNothing checks a disk that cannot be formatted leaves
// no file behind, under its name or another.
func TestFailedCreateLeavesNothing(t *testing.T) {
	needMke2fs(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.raw")
	if err := CreateExt4From(t.Context(), path, 4<<20, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("CreateExt4From of a directory that is not there succeeded")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed create left %v", entries)
	}
}

func TestAllocatedBytesOfAMissingFileIsZero(t *testing.T) {
	if got := AllocatedBytes(filepath.Join(t.TempDir(), "missing")); got != 0 {
		t.Errorf("AllocatedBytes = %d, want 0", got)
	}
}
