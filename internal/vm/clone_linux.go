// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile makes dst share src's blocks, copy-on-write, without copying
// them. It works on btrfs, XFS and bcachefs, and fails on everything else --
// which is the caller's cue to copy the data.
func cloneFile(src, dst *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}
