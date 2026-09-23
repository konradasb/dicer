// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import "embed"

//go:embed bin/arm64
var binFS embed.FS

const binDir = "bin/arm64"
