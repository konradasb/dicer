// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dicer-sh/dicer/internal/guest"
)

const (
	// configDiskDevice is the virtio block device that carries the config ext4 disk.
	configDiskDevice = "/dev/vdc"
	// configMountPoint is where the config disk is mounted to read the config file.
	configMountPoint = "/mnt/config"
)

// loadConfigDisk mounts the config disk read-only, reads filename from it,
// and returns the validated Config.
func loadConfigDisk(log *slog.Logger, filename string) (*guest.Config, error) {
	if err := os.MkdirAll(configMountPoint, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", configMountPoint, err)
	}

	if err := waitForDevice(configDiskDevice); err != nil {
		return nil, fmt.Errorf("config device: %w", err)
	}

	if err := syscall.Mount(configDiskDevice, configMountPoint, "ext4", syscall.MS_RDONLY, ""); err != nil {
		return nil, fmt.Errorf("mount config disk: %w", err)
	}
	log.Debug("config disk mounted", "dev", configDiskDevice)

	data, err := os.ReadFile(filepath.Join(configMountPoint, filename))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}

	var cfg guest.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	log.Info("config loaded", "cfg", cfg)
	return &cfg, nil
}
