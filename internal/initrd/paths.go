// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import "path/filepath"

// initrdDir returns the root directory for initrd storage.
func (m *Manager) initrdDir() string {
	return filepath.Join(m.cfg.DataDir, "initrd")
}

// buildDir returns the directory for a specific build ID.
func (m *Manager) buildDir(id string) string {
	return filepath.Join(m.initrdDir(), id)
}

// initrdPath returns the full path to an initrd binary.
func (m *Manager) initrdPath(id, arch string) string {
	return filepath.Join(m.buildDir(id), arch, "initrd")
}

// hashPath returns the path to the content hash file for a build.
func (m *Manager) hashPath(id, arch string) string {
	return filepath.Join(m.buildDir(id), arch, ".hash")
}

// latestLink returns the path to the "latest" symlink for an arch.
func (m *Manager) latestLink(arch string) string {
	return filepath.Join(m.initrdDir(), arch, "latest")
}
