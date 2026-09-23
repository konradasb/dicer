// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestExtract(t *testing.T) {
	dstPath := filepath.Join(t.TempDir(), string(DefaultVersion), "vmm")

	got, err := Extract(dstPath, DefaultVersion)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got != dstPath {
		t.Errorf("Extract() = %q, want %q", got, dstPath)
	}

	info, err := os.Stat(dstPath)
	if err != nil {
		t.Fatalf("stat extracted binary: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("mode = %v, want an executable", info.Mode())
	}
	if info.Size() == 0 {
		t.Error("extracted an empty binary")
	}
}

// TestExtractKeepsExistingBinary covers the common case: the binary was
// extracted by an earlier run, and must not be written again.
func TestExtractKeepsExistingBinary(t *testing.T) {
	dstPath := filepath.Join(t.TempDir(), "vmm")
	if err := os.WriteFile(dstPath, []byte("already here"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Extract(dstPath, DefaultVersion); err != nil {
		t.Fatalf("Extract over an existing binary: %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "already here" {
		t.Error("an existing binary was overwritten")
	}
}

// TestExtractUnknownVersion checks that a version Firecracker does not
// ship fails cleanly, leaving nothing behind.
func TestExtractUnknownVersion(t *testing.T) {
	dstPath := filepath.Join(t.TempDir(), "vmm")

	if _, err := Extract(dstPath, Version("v0.0.0")); err == nil {
		t.Fatal("Extract of an unshipped version succeeded")
	}
	if _, err := os.Stat(dstPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed Extract left %s behind", dstPath)
	}
}
