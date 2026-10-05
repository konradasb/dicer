// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// setHostname sets the kernel's hostname and writes /etc/hostname for an
// init system to read. It tries both, and returns what either failed with.
func setHostname(hostname string) error {
	var errs []error
	if err := syscall.Sethostname([]byte(hostname)); err != nil {
		errs = append(errs, fmt.Errorf("sethostname %q: %w", hostname, err))
	}
	path := filepath.Join(overlayRoot, "etc/hostname")
	if err := os.WriteFile(path, []byte(hostname+"\n"), 0o644); err != nil {
		errs = append(errs, fmt.Errorf("write %s: %w", path, err))
	}
	return errors.Join(errs...)
}
