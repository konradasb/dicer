// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package diskfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// reflink shares src's blocks with dst. It fails on filesystems without
// reflink support.
func reflink(src, dst *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}
