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
	"time"
)

// overlayRoot is the merged overlayfs mount point that becomes the guest rootfs.
const overlayRoot = "/overlay/newroot"

// overlayPath joins overlayRoot with the given path segments.
func overlayPath(parts ...string) string {
	return filepath.Join(append([]string{overlayRoot}, parts...)...)
}

// mountEarlyFilesystems performs best-effort mounts of /proc, /sys, and /dev.
// The Go runtime may need /proc before main starts; errors are silently
// ignored because the filesystems may already be mounted by an init.sh wrapper.
func mountEarlyFilesystems() {
	_ = os.MkdirAll("/proc", 0o755)
	_ = os.MkdirAll("/sys", 0o755)
	_ = os.MkdirAll("/dev", 0o755)
	_ = syscall.Mount("proc", "/proc", "proc", 0, "")
	_ = syscall.Mount("sysfs", "/sys", "sysfs", 0, "")
	_ = syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
}

// mountVirtualFilesystems mounts devpts, /dev/shm, and cgroup2 after /dev is ready.
func mountVirtualFilesystems(log *slog.Logger) error {
	if err := os.MkdirAll("/dev/pts", 0o755); err != nil {
		return fmt.Errorf("mkdir /dev/pts: %w", err)
	}
	if err := os.MkdirAll("/dev/shm", 0o755); err != nil {
		return fmt.Errorf("mkdir /dev/shm: %w", err)
	}
	if err := syscall.Mount("devpts", "/dev/pts", "devpts", 0, ""); err != nil {
		return fmt.Errorf("mount devpts: %w", err)
	}
	if err := syscall.Mount("tmpfs", "/dev/shm", "tmpfs", 0, "mode=1777,size=65536k"); err != nil {
		return fmt.Errorf("mount /dev/shm: %w", err)
	}

	// cgroup2 is required by container runtimes; non-fatal if the kernel lacks support.
	if err := os.MkdirAll("/sys/fs/cgroup", 0o755); err != nil {
		return fmt.Errorf("mkdir /sys/fs/cgroup: %w", err)
	}
	if err := syscall.Mount("cgroup2", "/sys/fs/cgroup", "cgroup2", 0, ""); err != nil {
		log.Warn("cgroup2 mount failed, continuing", "err", err)
	} else {
		log.Debug("mounted cgroup2")
	}

	return nil
}

// mountOverlayRootfs assembles the overlay rootfs used as the guest root:
//
//	/dev/vda → /lower         (erofs, read-only base image)
//	/dev/vdb → /overlay       (ext4, writable scratch disk)
//	overlayRoot               (merged overlayfs view)
func mountOverlayRootfs(log *slog.Logger) error {
	if err := waitForDevice("/dev/vda"); err != nil {
		return fmt.Errorf("rootfs device: %w", err)
	}
	if err := waitForDevice("/dev/vdb"); err != nil {
		return fmt.Errorf("overlay device: %w", err)
	}

	for _, dir := range []string{"/lower", "/overlay"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if err := syscall.Mount("/dev/vda", "/lower", "erofs", syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount /dev/vda: %w", err)
	}
	log.Debug("mounted rootfs", "dev", "/dev/vda", "target", "/lower")

	if err := syscall.Mount("/dev/vdb", "/overlay", "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount /dev/vdb: %w", err)
	}
	log.Debug("mounted overlay disk", "dev", "/dev/vdb", "target", "/overlay")

	for _, dir := range []string{"/overlay/upper", "/overlay/work", overlayRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	const opts = "lowerdir=/lower,upperdir=/overlay/upper,workdir=/overlay/work"
	if err := syscall.Mount("overlay", overlayRoot, "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}

	log.Info("overlay rootfs ready", "path", overlayRoot)
	return nil
}

// bindFilesystemsToNewRoot bind-mounts /proc, /sys, and /dev into the overlay
// rootfs so they are visible after chroot.
// /sys and /dev use MS_REC to include all sub-mounts (cgroup hierarchies, devpts).
func bindFilesystemsToNewRoot(log *slog.Logger) error {
	for _, d := range []string{"proc", "sys", "dev"} {
		if err := os.MkdirAll(overlayRoot+"/"+d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", overlayRoot+"/"+d, err)
		}
	}

	for _, m := range []struct {
		src   string
		dst   string
		flags uintptr
	}{
		{"/proc", overlayRoot + "/proc", syscall.MS_BIND},
		{"/sys", overlayRoot + "/sys", syscall.MS_BIND | syscall.MS_REC},
		{"/dev", overlayRoot + "/dev", syscall.MS_BIND | syscall.MS_REC},
	} {
		if err := syscall.Mount(m.src, m.dst, "", m.flags, ""); err != nil {
			return fmt.Errorf("bind %s to %s: %w", m.src, m.dst, err)
		}
	}

	// Standard /dev symlinks for process substitution and shell redirects.
	for _, s := range []struct{ target, link string }{
		{"/proc/self/fd", overlayRoot + "/dev/fd"},
		{"/proc/self/fd/0", overlayRoot + "/dev/stdin"},
		{"/proc/self/fd/1", overlayRoot + "/dev/stdout"},
		{"/proc/self/fd/2", overlayRoot + "/dev/stderr"},
	} {
		_ = os.Remove(s.link)
		_ = os.Symlink(s.target, s.link)
	}

	log.Debug("virtual filesystems bound into overlay root")
	return nil
}

// deviceTimeout is how long a block device the VMM attached may take to
// appear once the kernel is up.
const deviceTimeout = 2 * time.Second

// waitForDevice polls path until it exists or deviceTimeout elapses.
func waitForDevice(path string) error {
	const interval = 10 * time.Millisecond
	deadline := time.Now().Add(deviceTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(interval)
	}

	return fmt.Errorf("device %s not ready after %s", path, deviceTimeout)
}
