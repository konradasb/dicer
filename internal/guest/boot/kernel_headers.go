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
	modulesDir           = overlayRoot + "/lib/modules"
	kernelSourceDir      = overlayRoot + "/usr/src"
	kernelHeadersTarball = "/kernel-headers.tar.gz"
)

// extractKernelHeaders extracts the initrd's kernel headers into the overlay
// root and links them where DKMS looks for them. Modules and headers of other
// kernel releases are removed, so nothing builds against the wrong one. It
// returns nil if the initrd has no kernel headers.
func extractKernelHeaders(log *slog.Logger) error {
	if _, err := os.Stat(kernelHeadersTarball); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Debug("no kernel headers in the initrd")
			return nil
		}
		return fmt.Errorf("stat %s: %w", kernelHeadersTarball, err)
	}

	release, err := kernelRelease()
	if err != nil {
		return fmt.Errorf("read kernel release: %w", err)
	}
	log.Debug("kernel release", "release", release)

	if err := removeStaleModules(release); err != nil {
		log.Warn("stale module cleanup failed", "error", err)
	}
	if err := removeStaleKernelHeaders(release); err != nil {
		log.Warn("stale kernel header cleanup failed", "error", err)
	}

	kernelHeadersDir := filepath.Join(kernelSourceDir, "linux-headers-"+release)
	if err := os.MkdirAll(kernelHeadersDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", kernelHeadersDir, err)
	}

	out, err := exec.Command("/bin/tar", "-xzf", kernelHeadersTarball, "-C", kernelHeadersDir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("extract kernel headers: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// DKMS finds a release's headers through /lib/modules/<release>/build.
	releaseModulesDir := filepath.Join(modulesDir, release)
	if err := os.MkdirAll(releaseModulesDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", releaseModulesDir, err)
	}
	buildLink := filepath.Join(releaseModulesDir, "build")
	_ = os.Remove(buildLink)
	if err := os.Symlink("/usr/src/linux-headers-"+release, buildLink); err != nil {
		return fmt.Errorf("create build symlink: %w", err)
	}

	log.Info("kernel headers installed", "release", release, "dir", kernelHeadersDir)
	return nil
}

// kernelRelease returns the running kernel's release. It needs /proc
// mounted.
func kernelRelease() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// removeStaleModules removes the module directories of kernel releases other
// than release.
func removeStaleModules(release string) error {
	entries, err := os.ReadDir(modulesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", modulesDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != release {
			path := filepath.Join(modulesDir, entry.Name())
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove stale modules %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

// removeStaleKernelHeaders removes the kernel headers of releases other than
// release.
func removeStaleKernelHeaders(release string) error {
	entries, err := os.ReadDir(kernelSourceDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", kernelSourceDir, err)
	}
	want := "linux-headers-" + release
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "linux-headers-") && entry.Name() != want {
			path := filepath.Join(kernelSourceDir, entry.Name())
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove stale kernel headers %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}
