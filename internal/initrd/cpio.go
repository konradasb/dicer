// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/u-root/u-root/pkg/cpio"
)

// writeCPIO packs the tree under dir into an initramfs at path, and returns
// its size. The archive is left uncompressed so that the kernel boots from it
// without decompressing it first.
func writeCPIO(ctx context.Context, dir, path string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, fmt.Errorf("create the output directory: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("create the output file: %w", err)
	}
	defer func() { _ = f.Close() }()

	cpioWriter := cpio.Newc.Writer(f)
	recorder := cpio.NewRecorder()

	err = filepath.WalkDir(dir, func(entry string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		name, err := filepath.Rel(dir, entry)
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}

		record, err := recorder.GetRecord(entry)
		if err != nil {
			return fmt.Errorf("read %s: %w", entry, err)
		}
		record.Name = name

		if err := cpioWriter.WriteRecord(record); err != nil {
			return fmt.Errorf("write cpio record for %s: %w", entry, err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk %s: %w", dir, err)
	}

	if err := cpio.WriteTrailer(cpioWriter); err != nil {
		return 0, fmt.Errorf("write cpio trailer: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return info.Size(), nil
}
