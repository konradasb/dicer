// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package atomicfile writes files so that a crash cannot leave a partial one
// in place of a good one.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to path via a temporary file in the same directory,
// fsyncing before the rename. The rename is atomic, so a reader sees either
// the old contents or the new ones, never a truncated file.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()

	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp) // no-op once the rename has succeeded
	}()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}

	return nil
}
