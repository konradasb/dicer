// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dicer-sh/dicer/internal/guest"
)

// mountVolumes attaches each volume into the overlay rootfs.
// Per-volume errors are logged and skipped rather than aborting boot.
func mountVolumes(log *slog.Logger, mounts []guest.VolumeMount) {
	for _, vol := range mounts {
		target := filepath.Join(overlayRoot, vol.Path)
		if err := os.MkdirAll(target, 0o755); err != nil {
			log.Error("mkdir for volume failed", "path", vol.Path, "err", err)
			continue
		}

		var err error
		switch vol.Mode {
		case guest.MountModeRO:
			err = mountVolumeRO(vol, target)
		default: // MountModeRW
			err = mountVolumeRW(vol, target)
		}
		if err != nil {
			log.Error("volume mount failed", "path", vol.Path, "mode", vol.Mode, "err", err)
		} else {
			log.Info("volume mounted", "path", vol.Path, "mode", vol.Mode)
		}
	}
}

// mountVolumeRO mounts a volume read-only.
func mountVolumeRO(vol guest.VolumeMount, target string) error {
	// noload skips ext4 journal recovery, safe for read-only multi-attach.
	if err := syscall.Mount(vol.Device, target, vol.Fstype, syscall.MS_RDONLY, ext4Opt(vol.Fstype, "noload")); err != nil {
		return fmt.Errorf("mount %s ro: %w", vol.Device, err)
	}
	return nil
}

// mountVolumeRW mounts a volume read-write.
func mountVolumeRW(vol guest.VolumeMount, target string) error {
	if err := syscall.Mount(vol.Device, target, vol.Fstype, 0, ""); err != nil {
		return fmt.Errorf("mount %s rw: %w", vol.Device, err)
	}
	return nil
}

// ext4Opt returns opt when fstype is "ext4", otherwise returns empty string.
func ext4Opt(fstype, opt string) string {
	if fstype == "ext4" {
		return opt
	}

	return ""
}
