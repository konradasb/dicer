// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/konradasb/dicer/internal/atomicfile"
)

// The VMM binaries are downloaded into bin/$GOARCH/<version> by
// `make hypervisor-binaries`. Each architecture's file embeds only its own
// directory, so a binary never carries a VMM it cannot run.

// Extract atomically writes the embedded binary for version to dstPath,
// unless a file is already there, and returns dstPath.
func Extract(dstPath string, version Version) (string, error) {
	if _, err := os.Stat(dstPath); err == nil {
		return dstPath, nil
	}

	data, err := binaryFS.ReadFile(path.Join(binaryDir, string(version), "cloud-hypervisor"))
	if err != nil {
		return "", fmt.Errorf("read embedded binary: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return "", fmt.Errorf("create binaries dir: %w", err)
	}

	if err := atomicfile.Write(dstPath, data, 0o755); err != nil {
		return "", fmt.Errorf("write binary: %w", err)
	}

	return dstPath, nil
}
