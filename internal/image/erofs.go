// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// erofs packs a directory tree into a read-only compressed erofs filesystem image.
type erofs struct{}

func (erofs) Pack(ctx context.Context, dir, outputPath string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}

	// -zlz4: LZ4 fast compression (~20-25% space savings, faster builds)
	cmd := exec.CommandContext(ctx, "mkfs.erofs", "-zlz4", outputPath, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("mkfs.erofs: %w: %s", err, output)
	}

	stat, err := os.Stat(outputPath)
	if err != nil {
		return 0, fmt.Errorf("stat output: %w", err)
	}
	return stat.Size(), nil
}
