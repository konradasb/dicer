// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import "os"

// openConsole returns the first available virtio or serial console device,
// and its path; or stderr, and no path, if none can be opened.
func openConsole() (*os.File, string) {
	for _, path := range []string{"/dev/hvc0", "/dev/ttyAMA0", "/dev/ttyS0"} {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			continue
		}
		return file, path
	}
	return os.Stderr, ""
}

// console is where dicer-init logs. It is reopened when a write fails, since
// a workload's init may hang up the terminal. It is not safe for concurrent
// use: the slog handler writing to it serialises its writes.
type console struct {
	path string // empty if the console cannot be reopened
	file *os.File
}

// Write writes p to the console, reopening it once if the write fails.
func (c *console) Write(p []byte) (int, error) {
	n, err := c.file.Write(p)
	if err == nil || c.path == "" {
		return n, err
	}

	file, openErr := os.OpenFile(c.path, os.O_WRONLY, 0)
	if openErr != nil {
		return n, err
	}
	c.file = file // the old file is left open: os.Stdout and os.Stderr still refer to it
	return c.file.Write(p)
}
