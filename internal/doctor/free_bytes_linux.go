// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package doctor

import "syscall"

// freeBytes returns how much the filesystem holding path can still take.
func freeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * st.Bsize, nil
}
