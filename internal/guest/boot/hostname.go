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

// setHostname applies the configured hostname to the guest.
//
// Both halves are needed, for different readers. The syscall sets the name the
// kernel reports, which is what `hostname`, every program calling
// gethostname(2) and the shell prompt see. The file is what an init system
// reads when it comes up, so that the name survives the reboot of a guest
// running systemd.
//
// Writing only the file would leave a guest booted in exec mode -- no init
// system, nothing that reads /etc/hostname -- reporting whatever name the
// kernel was compiled with, which is neither the instance's nor anything the
// user chose.
func setHostname(hostname string) error {
	var errs []error

	if err := syscall.Sethostname([]byte(hostname)); err != nil {
		errs = append(errs, fmt.Errorf("sethostname %q: %w", hostname, err))
	}

	if err := writeHostnameFile(hostname); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// writeHostnameFile records the hostname in the guest's /etc/hostname.
func writeHostnameFile(hostname string) error {
	path := filepath.Join(overlayRoot, "etc/hostname")

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(hostname + "\n"); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
