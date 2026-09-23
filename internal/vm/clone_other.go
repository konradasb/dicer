// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build !linux

package vm

import (
	"errors"
	"os"
)

// cloneFile has no equivalent off Linux, so a copy is always a copy.
func cloneFile(_, _ *os.File) error {
	return errors.ErrUnsupported
}
