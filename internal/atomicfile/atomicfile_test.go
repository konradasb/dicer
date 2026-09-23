// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := Write(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("contents = %q, want hello", got)
	}
}

func TestWriteAppliesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")

	if err := Write(path, []byte("s3cret"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// TestWriteReplacesAtomically is the property the package exists for: a reader
// sees either the old contents or the new ones, never a half-written file.
func TestWriteReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := Write(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := Write(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("contents = %q, want second", got)
	}
}

// TestWriteLeavesNoTempFiles guards the rename path: a temporary file left
// behind would be picked up by a directory scan as a bogus entry.
func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"a.yaml", "b.yaml"} {
		if err := Write(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file %q was left behind", e.Name())
		}
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2", len(entries))
	}
}

func TestWriteFailsOnMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "config.yaml")

	if err := Write(path, []byte("x"), 0o600); err == nil {
		t.Error("expected an error when the parent directory does not exist")
	}
}

// TestWriteKeepsPreviousOnFailure checks that a failed write does not destroy
// what was already there.
func TestWriteKeepsPreviousOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := Write(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A directory cannot be renamed over by a file, so this write fails late,
	// after the temporary file has been created.
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := Write(blocked, []byte("new"), 0o600); err == nil {
		t.Fatal("expected an error writing over a directory")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "original" {
		t.Errorf("unrelated file was disturbed: %q", got)
	}
}

func TestWriteEmptyContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")

	if err := Write(path, nil, 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("size = %d, want 0", info.Size())
	}
}
