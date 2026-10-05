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

// erofs is the packer that makes the read-only, compressed EROFS disk a
// guest boots from.
type erofs struct{}

// Pack packs dir into an EROFS image at outputPath, creating its directory,
// and returns the image's size in bytes.
func (erofs) Pack(ctx context.Context, dir, outputPath string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		return 0, fmt.Errorf("create output directory: %w", err)
	}

	// -zlz4: LZ4 fast compression (~20-25% space savings, faster builds)
	cmd := exec.CommandContext(ctx, "mkfs.erofs", "-zlz4", outputPath, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("mkfs.erofs: %w: %s", err, output)
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		return 0, fmt.Errorf("stat output: %w", err)
	}
	return info.Size(), nil
}
