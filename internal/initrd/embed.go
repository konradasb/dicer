// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import "path"

// The guest binaries are built for the architecture dicerd itself is built
// for -- guests run on the same CPU as the host -- into bin/$GOARCH by
// `make build-embedded`. Each architecture's file embeds only its own
// directory, so a binary never carries guest code it cannot run.

// initBinary returns the embedded dicer-init binary.
func initBinary() ([]byte, error) {
	return binFS.ReadFile(path.Join(binDir, "dicer-init"))
}

// agentBinary returns the embedded dicer-agent binary.
func agentBinary() ([]byte, error) {
	return binFS.ReadFile(path.Join(binDir, "dicer-agent"))
}
