// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtractWritesTheKernelItsPinNames checks that the embedded default
// kernel decompresses to the kernel whose SHA-256 the package pins, so that
// the Makefile's download and Default never drift apart.
func TestExtractWritesTheKernelItsPinNames(t *testing.T) {
	dstPath := filepath.Join(t.TempDir(), string(DefaultVersion), "vmlinux")

	got, err := Extract(dstPath, DefaultVersion)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got != dstPath {
		t.Errorf("Extract() = %q, want %q", got, dstPath)
	}

	digest, err := fileSHA256(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if digest != Default().SHA256 {
		t.Errorf("the embedded kernel's SHA-256 is %s, want the pinned %s", digest, Default().SHA256)
	}
	if _, err := os.Stat(dstPath + ".partial"); !os.IsNotExist(err) {
		t.Error("Extract left its partial file behind")
	}
}

// TestExtractKeepsExistingBinary covers the common case: the kernel was
// extracted by an earlier run, and must not be written again.
func TestExtractKeepsExistingBinary(t *testing.T) {
	dstPath := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(dstPath, []byte("already here"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Extract(dstPath, DefaultVersion); err != nil {
		t.Fatalf("Extract over an existing kernel: %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "already here" {
		t.Errorf("the existing kernel was overwritten")
	}
}
