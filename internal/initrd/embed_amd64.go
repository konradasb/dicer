// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import "embed"

//go:embed bin/amd64
var binFS embed.FS

const binDir = "bin/amd64"
