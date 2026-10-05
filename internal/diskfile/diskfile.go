// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package diskfile makes, copies and measures disk files: the sparse files
// that hold a guest's disks on the host.
package diskfile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// statBlockSize is the unit of st_blocks.
const statBlockSize = 512

// CreateExt4 creates a sparse disk file of sizeBytes at path, holding an
// empty ext4 filesystem. It is formatted beside path and renamed into place,
// so path never holds a disk half made. Formatting needs mke2fs, from
// e2fsprogs.
func CreateExt4(ctx context.Context, path string, sizeBytes int64) error {
	return create(ctx, path, sizeBytes, "")
}

// CreateExt4From is CreateExt4 with the filesystem holding a copy of the
// files in dir. It needs e2fsprogs 1.43 or later.
func CreateExt4From(ctx context.Context, path string, sizeBytes int64, dir string) error {
	return create(ctx, path, sizeBytes, dir)
}

// create makes the disk file at path, filled from dir unless dir is empty.
func create(ctx context.Context, path string, sizeBytes int64, dir string) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create disk directory: %w", err)
	}

	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create disk file: %w", err)
	}
	partial := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(partial)
		}
	}()

	if err := f.Truncate(sizeBytes); err != nil {
		_ = f.Close()
		return fmt.Errorf("allocate disk: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close disk file: %w", err)
	}

	args := []string{"-t", "ext4", "-F", "-q"}
	if dir != "" {
		args = append(args, "-d", dir)
	}
	if out, err := exec.CommandContext(ctx, "mke2fs", append(args, partial)...).CombinedOutput(); err != nil {
		return fmt.Errorf("format disk: %w: %s", err, out)
	}

	if err := os.Rename(partial, path); err != nil {
		return fmt.Errorf("install disk: %w", err)
	}
	return nil
}

// AllocatedBytes returns the space the file at path takes up on the host,
// which for a sparse file is less than its size, or 0 if there is no file.
func AllocatedBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Blocks * statBlockSize
	}
	return info.Size()
}

// AllocatedBytesUnder returns the space the files in the directory tree at
// dir take up on the host, by AllocatedBytes. It returns what it counted
// with the first error met walking the tree.
func AllocatedBytesUnder(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		total += AllocatedBytes(path)
		return nil
	})
	return total, err
}
