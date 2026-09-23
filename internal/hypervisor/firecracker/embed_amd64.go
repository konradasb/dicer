// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import "embed"

//go:embed bin/amd64
var binaryFS embed.FS

const binaryDir = "bin/amd64"
