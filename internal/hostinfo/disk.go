// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hostinfo

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/dicer-sh/dicer"
)

// ReadDiskUsage reports on the filesystem holding path.
func ReadDiskUsage(path string) (*dicer.DiskUsage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return nil, fmt.Errorf("statfs %s: %w", path, err)
	}

	blockSize := toInt64(st.Bsize)
	return &dicer.DiskUsage{
		TotalBytes: int64(st.Blocks) * blockSize,
		FreeBytes:  int64(st.Bavail) * blockSize,
	}, nil
}

// toInt64 converts a statfs field whose type differs between platforms:
// Bsize is an int64 on Linux and a uint32 on Darwin.
func toInt64[T ~int64 | ~uint32](v T) int64 {
	return int64(v)
}
