// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package kernel

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

// The default kernel is downloaded into bin/$GOARCH/<version> by
// `make guest-binaries`, and compressed with zstd. Each architecture's file
// embeds only its own directory, so a binary never carries a kernel it
// cannot boot.

// Extract writes the default kernel of version, decompressed, to dstPath,
// unless a file is already there, and returns dstPath. The kernel is
// written beside dstPath and renamed into place, so dstPath is never a
// partial kernel.
func Extract(dstPath string, version Version) (string, error) {
	if _, err := os.Stat(dstPath); err == nil {
		return dstPath, nil
	}

	compressed, err := binaryFS.Open(path.Join(binaryDir, string(version), "vmlinux.zst"))
	if err != nil {
		return "", fmt.Errorf("read embedded kernel: %w", err)
	}
	defer func() { _ = compressed.Close() }()

	decoder, err := zstd.NewReader(compressed)
	if err != nil {
		return "", fmt.Errorf("decompress embedded kernel: %w", err)
	}
	defer decoder.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return "", fmt.Errorf("create kernel directory: %w", err)
	}

	partial := dstPath + ".partial"
	defer func() { _ = os.Remove(partial) }()

	if _, err := createFile(partial, decoder); err != nil {
		return "", fmt.Errorf("write kernel: %w", err)
	}
	if err := os.Rename(partial, dstPath); err != nil {
		return "", fmt.Errorf("install kernel: %w", err)
	}

	return dstPath, nil
}
