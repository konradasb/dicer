// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/konradasb/dicer/internal/atomicfile"
)

// guestAgentPath is where the guest agent is, both in the initrd and, once
// installed, in the guest's root.
const guestAgentPath = "/usr/local/bin/dicer-agent"

// guestAgentUnit is the systemd unit that runs the guest agent.
const guestAgentUnit = `[Unit]
Description=Dicer Agent
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=` + guestAgentPath + `
EnvironmentFile=-/etc/dicer/env
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`

// installGuestAgent atomically copies the guest agent from the initrd into
// the overlay root, unless an identical copy is already installed.
func installGuestAgent(log *slog.Logger) error {
	want, err := os.ReadFile(guestAgentPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestAgentPath, err)
	}

	dst := filepath.Join(overlayRoot, guestAgentPath)
	same, err := hasContents(dst, want)
	if err != nil {
		return err
	}
	if same {
		log.Debug("guest agent already installed", "path", guestAgentPath)
		return nil
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := atomicfile.Write(dst, want, 0o755); err != nil {
		return fmt.Errorf("install guest agent: %w", err)
	}
	// So that the rename survives a guest that is killed rather than shut
	// down.
	if err := syncDir(dir); err != nil {
		return err
	}

	log.Info("guest agent installed", "path", guestAgentPath)
	return nil
}

// injectGuestAgentUnit writes the guest agent's systemd unit, enabled, and
// the environment file it reads into the overlay root.
func injectGuestAgentUnit(env map[string]string) error {
	const (
		unitDir  = overlayRoot + "/etc/systemd/system"
		wantsDir = unitDir + "/multi-user.target.wants"
		envDir   = overlayRoot + "/etc/dicer"
	)

	for _, dir := range []string{unitDir, wantsDir, envDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(envDir+"/env", []byte(envFileContents(env)), 0o644); err != nil {
		return fmt.Errorf("write environment file: %w", err)
	}
	if err := os.WriteFile(unitDir+"/dicer-agent.service", []byte(guestAgentUnit), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	link := wantsDir + "/dicer-agent.service"
	if err := os.Symlink("../dicer-agent.service", link); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("enable unit: %w", err)
	}
	return nil
}

// hasContents reports whether the file at path holds exactly want. A missing
// file does not.
func hasContents(path string, want []byte) (bool, error) {
	got, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return bytes.Equal(got, want), nil
}

// syncDir flushes a directory's entries to disk.
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = dir.Close() }()

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}
