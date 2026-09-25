// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import "path/filepath"

// initrdDir returns the root directory for initrd storage.
func (m *Manager) initrdDir() string {
	return filepath.Join(m.cfg.DataDir, "initrd")
}

// archDir returns the directory holding the initrd for an arch.
func (m *Manager) archDir(arch string) string {
	return filepath.Join(m.initrdDir(), arch)
}

// initrdPath returns the path to the initrd for an arch.
func (m *Manager) initrdPath(arch string) string {
	return filepath.Join(m.archDir(arch), initrdFilename)
}

// hashPath returns the path to the content hash of the initrd for an arch.
func (m *Manager) hashPath(arch string) string {
	return filepath.Join(m.archDir(arch), hashFilename)
}
