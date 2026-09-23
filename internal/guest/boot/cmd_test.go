// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"os"
	"path/filepath"
	"testing"
)

// A console taken over under dicer-init -- the file it opened no longer
// writable -- is reopened, and the write goes through.
func TestConsoleReopensWhenAWriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console")
	stale, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = stale.Close() // as good as hung up: every write to it fails

	c := &console{path: path, f: stale}
	if _, err := c.Write([]byte("the workload exited\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the workload exited\n" {
		t.Errorf("console = %q, want the line written after reopening", got)
	}
}
