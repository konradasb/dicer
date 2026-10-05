// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build !linux

package diskfile

import (
	"errors"
	"os"
)

// reflink is unsupported off Linux.
func reflink(_, _ *os.File) error {
	return errors.ErrUnsupported
}
