// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build !linux

package vm

import (
	"errors"
	"os"
)

// cloneFile is unsupported off Linux.
func cloneFile(_, _ *os.File) error {
	return errors.ErrUnsupported
}
