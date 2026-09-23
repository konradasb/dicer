// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	securejoin "github.com/cyphar/filepath-securejoin"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/types"
)

// filesDir is a tmpfs in the initramfs that holds the contents of mounted
// host files, each bind-mounted onto its target. It is outside the guest's
// root, so the guest sees only the targets, and nothing is written to disk.
const filesDir = "/dicer/files"

// mountAll mounts the guest's mount table inside the overlay root, parents
// before children, so a mount can go inside another. A mount that fails is
// logged and skipped rather than aborting boot.
func mountAll(log *slog.Logger, mounts []guest.Mount, mode types.InitMode) {
	if len(mounts) == 0 {
		return
	}

	if mode == types.ModeSystemd && slices.ContainsFunc(mounts, underRun) {
		// systemd mounts a tmpfs on /run only if nothing is mounted there
		// yet. Mounting it first, as an initrd would, keeps the mounts
		// beneath it from being hidden.
		if err := mountRun(); err != nil {
			log.Error("mount /run failed", "err", err)
		}
	}

	if slices.ContainsFunc(mounts, func(m guest.Mount) bool { return m.File != nil }) {
		if err := mountFilesDir(); err != nil {
			log.Error("mount failed", "target", filesDir, "err", err)
		}
	}

	sorted := slices.Clone(mounts)
	slices.SortStableFunc(sorted, func(a, b guest.Mount) int {
		return cmp.Compare(depth(a.Target), depth(b.Target))
	})

	for i, m := range sorted {
		if err := mountOne(i, m); err != nil {
			log.Error("mount failed", "target", m.Target, "err", err)
			continue
		}
		log.Info("mounted", "target", m.Target, "read_only", m.ReadOnly)
	}
}

// mountOne mounts m, the i-th in mount order, at its target in the overlay
// root. The target is resolved inside the root, so a symlink in the image
// cannot send it into the initramfs.
func mountOne(i int, m guest.Mount) error {
	target, err := securejoin.SecureJoin(overlayRoot, m.Target)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}

	switch {
	case m.Volume != nil:
		return mountVolume(m.Volume, target, m.ReadOnly)
	case m.File != nil:
		return mountFile(i, m.File, target, m.ReadOnly)
	case m.Tmpfs != nil:
		return mountTmpfs(target)
	default:
		return errors.New("nothing to mount")
	}
}

// mountVolume mounts the filesystem on a volume's disk.
func mountVolume(vol *guest.VolumeSource, target string, readOnly bool) error {
	if err := waitForDevice(vol.Device); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}

	var (
		flags uintptr
		data  string
	)
	if readOnly {
		flags = syscall.MS_RDONLY
		// noload skips ext4 journal recovery, which a disk other guests
		// may also have attached must not get.
		if vol.Fstype == "ext4" {
			data = "noload"
		}
	}

	if err := syscall.Mount(vol.Device, target, vol.Fstype, flags, data); err != nil {
		return fmt.Errorf("mount %s: %w", vol.Device, err)
	}
	return nil
}

// mountTmpfs mounts an empty tmpfs, world-writable and sticky as /tmp is.
func mountTmpfs(target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	if err := syscall.Mount("tmpfs", target, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
		return fmt.Errorf("mount tmpfs: %w", err)
	}
	return nil
}

// mountFile writes a host file's contents to filesDir and bind-mounts it
// onto target, creating an empty file there first if there is none.
func mountFile(i int, file *guest.FileSource, target string, readOnly bool) error {
	src := filepath.Join(filesDir, strconv.Itoa(i))
	if err := os.WriteFile(src, file.Data, 0o600); err != nil {
		return fmt.Errorf("write contents: %w", err)
	}
	if err := os.Chown(src, file.UID, file.GID); err != nil {
		return fmt.Errorf("chown contents: %w", err)
	}
	// After the chown, which clears setuid and setgid bits.
	if err := os.Chmod(src, fs.FileMode(file.Mode).Perm()); err != nil {
		return fmt.Errorf("chmod contents: %w", err)
	}

	if err := ensureFile(target); err != nil {
		return err
	}
	if err := syscall.Mount(src, target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind mount: %w", err)
	}
	if readOnly {
		const flags = syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY
		if err := syscall.Mount("", target, "", flags, ""); err != nil {
			return fmt.Errorf("remount read-only: %w", err)
		}
	}
	return nil
}

// mountFilesDir mounts the tmpfs at filesDir.
func mountFilesDir() error {
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return err
	}
	return syscall.Mount("tmpfs", filesDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0700")
}

// ensureFile makes sure there is a file at path to bind-mount onto.
func ensureFile(path string) error {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return fmt.Errorf("%s is a directory in the image", path)
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// mountRun mounts a tmpfs on /run with the options systemd would give it.
func mountRun() error {
	target := overlayPath("run")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return syscall.Mount("tmpfs", target, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0755")
}

// underRun reports whether m's target is inside /run.
func underRun(m guest.Mount) bool {
	return strings.HasPrefix(filepath.Clean(m.Target)+"/", "/run/")
}

// depth is how many path components are in target.
func depth(target string) int {
	return strings.Count(filepath.Clean(target), "/")
}
