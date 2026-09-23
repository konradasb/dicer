// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build !linux

package process

import "errors"

// Attach needs pidfds to watch a process that is not its child, and those
// exist only on Linux, which is the only place the daemon runs.
func Attach(int, string) (*Process, error) {
	return nil, errors.ErrUnsupported
}
