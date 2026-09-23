// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile reflinks src's blocks into dst. It fails on filesystems without
// reflink support.
func cloneFile(src, dst *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}
