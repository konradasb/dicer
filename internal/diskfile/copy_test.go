// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package diskfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestCopyKeepsHolesAndData checks a copy holds what the original held, a
// leading hole and a trailing one included, and leaves nothing else behind.
func TestCopyKeepsHolesAndData(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "overlay.raw")
	want := append(make([]byte, 2*copyChunkSize), []byte("guest data")...)
	want = append(want, make([]byte, copyChunkSize)...)
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "snapshots", "overlay.raw")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the copy is %d bytes, want %d identical bytes", len(got), len(want))
	}

	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d files, want only the copy", len(entries))
	}
}

func TestCopyOfAMissingFileFails(t *testing.T) {
	dir := t.TempDir()
	if err := Copy(filepath.Join(dir, "missing"), filepath.Join(dir, "copy")); err == nil {
		t.Error("Copy of a missing file succeeded")
	}
}

// TestCopyOfALargeSparseDiskKeepsItsData checks a disk mostly made of holes,
// as an instance's is: its data must arrive where it was, and its holes stay
// holes.
func TestCopyOfALargeSparseDiskKeepsItsData(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "overlay.raw")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	const size, at = 16 << 30, 8 << 30
	data := []byte("guest data in the middle")
	if _, err := f.WriteAt(data, at); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "copy.raw")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	copied, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copied.Close() }()
	got := make([]byte, len(data))
	if _, err := copied.ReadAt(got, at); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("the copy holds %q at %d, want %q", got, at, data)
	}
	info, err := copied.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Errorf("the copy is %d bytes, want %d", info.Size(), size)
	}
	if allocated := AllocatedBytes(dst); allocated > 16<<20 {
		t.Errorf("the copy takes %d bytes on disk, want its holes kept", allocated)
	}
}
