// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package diskfile

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// skipUnlessInstalled skips a test on a host without every one of programs.
func skipUnlessInstalled(t *testing.T, programs ...string) {
	t.Helper()

	for _, program := range programs {
		if _, err := exec.LookPath(program); err != nil {
			t.Skipf("%s is not installed", program)
		}
	}
}

// TestCreateExt4MakesASparseDisk checks the disk file is the size asked for,
// takes up far less than that, and is left alone in its directory.
func TestCreateExt4MakesASparseDisk(t *testing.T) {
	skipUnlessInstalled(t, "mke2fs")

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
	skipUnlessInstalled(t, "mke2fs", "debugfs")

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
	skipUnlessInstalled(t, "mke2fs")

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

// TestAllocatedBytesUnderSumsTheTree checks every file in the tree is counted,
// those in subdirectories too.
func TestAllocatedBytesUnderSumsTheTree(t *testing.T) {
	dir := t.TempDir()
	files := []string{filepath.Join(dir, "vmstate"), filepath.Join(dir, "disks", "overlay.raw")}
	var want int64
	for _, path := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 64<<10), 0o600); err != nil {
			t.Fatal(err)
		}
		want += AllocatedBytes(path)
	}

	got, err := AllocatedBytesUnder(dir)
	if err != nil {
		t.Fatalf("AllocatedBytesUnder: %v", err)
	}
	if want == 0 || got != want {
		t.Errorf("AllocatedBytesUnder = %d, want the files' %d", got, want)
	}
}

func TestAllocatedBytesUnderAMissingDirectoryFails(t *testing.T) {
	if _, err := AllocatedBytesUnder(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("AllocatedBytesUnder of a missing directory succeeded")
	}
}
