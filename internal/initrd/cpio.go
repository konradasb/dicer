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

// cpioPacker packs a directory tree into an uncompressed cpio archive (initramfs format).
// Uses uncompressed format for faster boot — the kernel loads it directly without decompression.
type cpioPacker struct{}

func (cpioPacker) Pack(ctx context.Context, dir, outputPath string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("create output file: %w", err)
	}
	defer func() { _ = outFile.Close() }()

	cpioWriter := cpio.Newc.Writer(outFile)
	recorder := cpio.NewRecorder()

	err = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		rec, err := recorder.GetRecord(path)
		if err != nil {
			return fmt.Errorf("get cpio record for %s: %w", path, err)
		}
		rec.Name = relPath

		if err := cpioWriter.WriteRecord(rec); err != nil {
			return fmt.Errorf("write cpio record for %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk dir: %w", err)
	}

	if err := cpio.WriteTrailer(cpioWriter); err != nil {
		return 0, fmt.Errorf("write cpio trailer: %w", err)
	}

	stat, err := os.Stat(outputPath)
	if err != nil {
		return 0, fmt.Errorf("stat output: %w", err)
	}
	return stat.Size(), nil
}
