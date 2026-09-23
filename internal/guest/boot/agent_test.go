// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"os"
	"path/filepath"
	"testing"
)

// An installed agent the same size as the initrd's is still replaced if it
// differs: two builds are easily the same size, and an instance must get the
// agent of the daemon that booted it.
func TestSameContentsComparesBytesNotSizes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new")
	installed := filepath.Join(dir, "old")

	if err := os.WriteFile(src, []byte("agent v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("agent v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	if same, err := sameContents(src, installed); err != nil || same {
		t.Errorf("sameContents of two same-sized agents = %v, %v; want false", same, err)
	}

	if err := os.WriteFile(installed, []byte("agent v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if same, err := sameContents(src, installed); err != nil || !same {
		t.Errorf("sameContents of identical agents = %v, %v; want true", same, err)
	}

	if same, err := sameContents(src, filepath.Join(dir, "missing")); err != nil || same {
		t.Errorf("sameContents with nothing installed = %v, %v; want false", same, err)
	}
}
