// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import "embed"

//go:embed bin/arm64
var binaryFS embed.FS

const binaryDir = "bin/arm64"
