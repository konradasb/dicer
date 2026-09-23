// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	modulesDir     = overlayRoot + "/lib/modules"
	kernelSrcDir   = overlayRoot + "/usr/src"
	headersTarball = "/kernel-headers.tar.gz"
)

// extractKernelHeaders extracts headers from the initrd tarball into the overlay
// rootfs and creates the build symlink expected by DKMS. Stale headers from
// other kernel versions are removed to prevent version conflicts.
//
// Returns nil without error if no tarball is present.
func extractKernelHeaders(log *slog.Logger) error {
	if _, err := os.Stat(headersTarball); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Debug("no kernel-headers.tar.gz found, skipping")
			return nil
		}
		return fmt.Errorf("stat %s: %w", headersTarball, err)
	}

	release, err := kernelRelease()
	if err != nil {
		return fmt.Errorf("read kernel release: %w", err)
	}
	log.Debug("kernel release", "release", release)

	if err := removeStaleModules(release); err != nil {
		log.Warn("stale module cleanup failed", "err", err)
	}
	if err := removeStaleHeaders(release); err != nil {
		log.Warn("stale header cleanup failed", "err", err)
	}

	headersDir := filepath.Join(kernelSrcDir, "linux-headers-"+release)
	if err := os.MkdirAll(headersDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", headersDir, err)
	}

	out, err := exec.Command("/bin/tar", "-xzf", headersTarball, "-C", headersDir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("extract headers: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Create the build symlink in /lib/modules/<release>/build pointing to
	// /usr/src/linux-headers-<release>. DKMS looks here when building modules.
	kModulesDir := filepath.Join(modulesDir, release)
	if err := os.MkdirAll(kModulesDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", kModulesDir, err)
	}
	buildLink := filepath.Join(kModulesDir, "build")
	_ = os.Remove(buildLink)
	if err := os.Symlink("/usr/src/linux-headers-"+release, buildLink); err != nil {
		return fmt.Errorf("create build symlink: %w", err)
	}

	log.Info("kernel headers installed", "release", release, "dir", headersDir)
	return nil
}

// kernelRelease returns the running kernel version string.
// /proc must be mounted before calling this.
func kernelRelease() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// removeStaleModules removes /lib/modules/* directories that do not match release.
func removeStaleModules(release string) error {
	entries, err := os.ReadDir(modulesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("readdir %s: %w", modulesDir, err)
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != release {
			path := filepath.Join(modulesDir, e.Name())
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove stale modules %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}

// removeStaleHeaders removes /usr/src/linux-headers-* directories that do not
// match release.
func removeStaleHeaders(release string) error {
	entries, err := os.ReadDir(kernelSrcDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("readdir %s: %w", kernelSrcDir, err)
	}
	want := "linux-headers-" + release
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "linux-headers-") && e.Name() != want {
			path := filepath.Join(kernelSrcDir, e.Name())
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove stale headers %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}
