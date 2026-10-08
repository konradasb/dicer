// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/diskfile"
	"github.com/konradasb/dicer/internal/guest"
)

// ensureOverlayDisk creates the writable ext4 overlay disk if it does not
// exist, and grows one smaller than sizeBytes: one the instance's disk_bytes
// has been raised for, or one a snapshot restored at its older size.
func ensureOverlayDisk(ctx context.Context, path string, sizeBytes int64) error {
	if _, err := os.Stat(path); err == nil {
		if err := diskfile.GrowExt4(ctx, path, sizeBytes); err != nil {
			return fmt.Errorf("grow overlay disk: %w", err)
		}
		return nil
	}
	if err := diskfile.CreateExt4(ctx, path, sizeBytes); err != nil {
		return fmt.Errorf("create overlay disk: %w", err)
	}
	return nil
}

// writeStatusDisk writes guestStatus to the status disk. See guest.Status.
func writeStatusDisk(path string, guestStatus guest.Status) error {
	if err := atomicfile.Write(path, guestStatus.Encode(), 0o600); err != nil {
		return fmt.Errorf("write status disk: %w", err)
	}
	return nil
}

// readStatusDisk reads what the guest reported of its end.
func readStatusDisk(path string) (guest.Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return guest.Status{}, fmt.Errorf("read status disk: %w", err)
	}
	return guest.DecodeStatus(data)
}

// configDiskSize is the size of the sparse config disk, before room for the
// config itself, which holds the contents of every file mount.
const configDiskSize = 4 << 20

// provisionConfigDisk creates the ext4 disk holding dicer-init's
// configuration. The config may hold secrets, so it is staged in the
// instance's runtime directory.
func provisionConfigDisk(ctx context.Context, path string, cfg *guest.Config) error {
	stagingDir, err := os.MkdirTemp(filepath.Dir(path), "config-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, guest.ConfigFile), data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", guest.ConfigFile, err)
	}

	if err := diskfile.CreateExt4From(ctx, path, configDiskSize+2*int64(len(data)), stagingDir); err != nil {
		return fmt.Errorf("create config disk: %w", err)
	}
	return nil
}
