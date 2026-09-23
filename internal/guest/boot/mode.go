// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package boot

import (
	"os"
	"path/filepath"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/guest"
)

// guestPath is the PATH the workload runs with, and the one a command
// without a slash is looked up on.
const guestPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// resolveMode decides how to start the workload, for a guest whose root
// filesystem is at root. A mode the config names is kept; auto is systemd if
// the command resolves to the systemd binary, and exec otherwise.
//
// The decision is made here, in the guest, because only here is the root
// filesystem at hand: /sbin/init is systemd on Debian and something else on
// Alpine, and only following the symlink tells which.
func resolveMode(root string, cfg *guest.Config) dicer.InitMode {
	if cfg.Mode != dicer.ModeAuto {
		return cfg.Mode
	}
	if isSystemd(root, cfg.Argv()[0]) {
		return dicer.ModeSystemd
	}
	return dicer.ModeExec
}

// isSystemd reports whether command, run in the guest whose root is at root,
// is the systemd binary: an executable .../systemd/systemd, symlinks
// followed within the root. A symlink that would leave the root is followed
// as the guest would, from the root, rather than out of it.
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
