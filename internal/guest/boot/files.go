// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/guest"
)

const (
	// guestFilesDir is where injected host files appear in the guest.
	// /run/secrets is the conventional location images already look in.
	guestFilesDir = "/run/secrets"

	// stagingDir holds the files until early boot in systemd mode, when
	// dicer-files.service copies them onto the tmpfs.
	stagingDir = "/etc/dicer/.files-staging"
)

// filesServiceUnit is a systemd unit that mounts a tmpfs at /run/secrets and
// copies the staged files into it. It is needed because in systemd mode /run
// is a tmpfs mounted after dicer-init runs, which would hide anything placed
// there beforehand.
const filesServiceUnit = `[Unit]
Description=Mount injected host files into /run/secrets
DefaultDependencies=no
Before=sysinit.target
After=local-fs.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c '\
  set -e; \
  mkdir -p /run/secrets; \
  mount -t tmpfs -o noexec,nosuid,nodev,mode=0755 tmpfs /run/secrets; \
  staging=` + stagingDir + `; \
  if [ -d "$staging" ]; then \
    for f in "$staging"/*; do \
      [ -f "$f" ] && cp "$f" /run/secrets/ && chmod 0400 /run/secrets/"$(basename "$f")"; \
    done; \
    rm -rf "$staging"; \
  fi'

[Install]
WantedBy=sysinit.target
`

// mountFiles writes the injected host files into the overlay root.
//
// In exec mode it creates a tmpfs at /run/secrets and writes each file
// directly -- the tmpfs exists only in RAM.
//
// In systemd mode, systemd mounts a fresh tmpfs over /run during early boot,
// which wipes any files placed there beforehand. Instead, files are staged to
// /etc/dicer/.files-staging and a oneshot systemd unit (dicer-files.service)
// creates a tmpfs at /run/secrets, copies the files, and removes the staging
// directory.
func mountFiles(files []guest.FileMount, mode dicer.InitMode) error {
	if len(files) == 0 {
		return nil
	}

	if mode != dicer.ModeSystemd {
		return mountFilesExec(files)
	}

	return mountFilesSystemd(files)
}

// mountFilesExec mounts a tmpfs at /run/secrets and writes the files into it.
// This is used in exec mode where dicer-init is responsible for the full boot sequence and there is no init system to defer the mount to.
func mountFilesExec(files []guest.FileMount) error {
	target := overlayPath(guestFilesDir)

	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}

	const flags = syscall.MS_NOEXEC | syscall.MS_NOSUID | syscall.MS_NODEV
	if err := syscall.Mount("tmpfs", target, "tmpfs", flags, "mode=0755"); err != nil {
		return fmt.Errorf("mount tmpfs on %s: %w", target, err)
	}

	return writeFiles(target, files)
}

// mountFilesSystemd stages the files and injects a systemd unit to copy them
// into a tmpfs at /run/secrets during early boot.
// This is used in systemd mode where /run is a tmpfs that is mounted after dicer-init runs, which would hide any files placed there beforehand.
func mountFilesSystemd(files []guest.FileMount) error {
	staging := overlayPath(stagingDir)

	if err := os.MkdirAll(staging, 0o700); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}

	if err := writeFiles(staging, files); err != nil {
		return err
	}

	return injectFilesUnit()
}

// writeFiles writes each file with mode 0400 into dir.
func writeFiles(dir string, files []guest.FileMount) error {
	for _, s := range files {
		path := filepath.Join(dir, s.Name)
		if err := os.WriteFile(path, s.Value, 0o400); err != nil {
			return fmt.Errorf("write file %q: %w", s.Name, err)
		}
	}

	return nil
}

// injectFilesUnit writes the systemd unit that mounts the tmpfs and copies
// the staged files into it during early boot. Only needed in systemd mode.
func injectFilesUnit() error {
	unitDir := overlayPath("etc/systemd/system")
	wantsDir := filepath.Join(unitDir, "sysinit.target.wants")

	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", unitDir, err)
	}

	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", wantsDir, err)
	}

	unitPath := filepath.Join(unitDir, "dicer-files.service")
	if err := os.WriteFile(unitPath, []byte(filesServiceUnit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}

	link := filepath.Join(wantsDir, "dicer-files.service")
	if err := os.Symlink("../dicer-files.service", link); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("symlink unit: %w", err)
	}

	return nil
}
