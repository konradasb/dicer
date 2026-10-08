// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package kernel

import (
	"embed"

	"github.com/konradasb/dicer/internal/types"
)

//go:embed bin/arm64
var binaryFS embed.FS

const binaryDir = "bin/arm64"

// The default kernel's architecture, and the SHA-256 of DefaultVersion's
// kernel, decompressed.
const (
	defaultArchitecture = types.ArchitectureAArch64
	defaultSHA256       = "1ce335854bc05535584dd57638f10832db91c4a20cbb76bab7851890c3d14568"
)
