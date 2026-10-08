// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package kernel

import (
	"embed"

	"github.com/konradasb/dicer/internal/types"
)

//go:embed bin/amd64
var binaryFS embed.FS

const binaryDir = "bin/amd64"

// The default kernel's architecture, and the SHA-256 of DefaultVersion's
// kernel, decompressed.
const (
	defaultArchitecture = types.ArchitectureX86_64
	defaultSHA256       = "ca5db6c291deb8a409db1f1ab14cc55ef6d35504daf17fc0f5577ffc1662b669"
)
