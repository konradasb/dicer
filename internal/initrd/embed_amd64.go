// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import "embed"

//go:embed bin/amd64
var binaries embed.FS

const binariesDir = "bin/amd64"
