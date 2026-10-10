// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build !linux

package doctor

import "errors"

// freeBytes is Linux's only, where the daemon runs.
func freeBytes(string) (int64, error) {
	return 0, errors.New("the daemon's disk can be checked only on Linux")
}
