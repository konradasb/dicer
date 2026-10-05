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
