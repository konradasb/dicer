// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import "path"

// dicer-init and dicer-agent are built into bin/$GOARCH by
// `make guest-binaries`. Each architecture's file embeds only its own
// directory.

// embeddedBinary returns the embedded guest binary of a name, such as
// dicer-init.
func embeddedBinary(name string) ([]byte, error) {
	return binaries.ReadFile(path.Join(binariesDir, name))
}
