// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package boot

import (
	"os"
	"path/filepath"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/types"
)

// guestPath is the PATH the workload runs with, and the one a command
// without a slash is looked up on.
const guestPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// resolveMode resolves auto to systemd if the command is the systemd binary
// in the root filesystem at root, and to exec otherwise.
func resolveMode(root string, cfg *guest.Config) types.InitMode {
	if cfg.Mode != types.ModeAuto {
		return cfg.Mode
	}
	if isSystemd(root, cfg.Argv()[0]) {
		return types.ModeSystemd
	}
	return types.ModeExec
}

// isSystemd reports whether command resolves to .../systemd/systemd within
// root, following symlinks as the guest would.
func isSystemd(root, command string) bool {
	path, ok := lookPath(root, command)
	if !ok {
		return false
	}

	resolved, err := securejoin.SecureJoin(root, path)
	if err != nil || !isExecutable(resolved) {
		return false
	}
	return filepath.Base(resolved) == "systemd" && filepath.Base(filepath.Dir(resolved)) == "systemd"
}

// lookPath finds command as the guest would run it, relative to its root: as
// it is, if it names a path; else on guestPath. The path it returns is the
// guest's, before any symlink in it is followed.
func lookPath(root, command string) (string, bool) {
	if strings.Contains(command, "/") {
		return command, true
	}

	for _, dir := range filepath.SplitList(guestPath) {
		candidate := filepath.Join(dir, command)
		full, err := securejoin.SecureJoin(root, candidate)
		if err != nil {
			continue
		}
		if isExecutable(full) {
			return candidate, true
		}
	}
	return "", false
}

// isExecutable reports whether path is a regular file anyone may execute.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
