// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import "path"

// The guest binaries are built into bin/$GOARCH by `make embedded`. Each
// architecture's file embeds only its own directory.

// initBinary returns the embedded dicer-init binary.
func initBinary() ([]byte, error) {
	return binFS.ReadFile(path.Join(binDir, "dicer-init"))
}

// agentBinary returns the embedded dicer-agent binary.
func agentBinary() ([]byte, error) {
	return binFS.ReadFile(path.Join(binDir, "dicer-agent"))
}
