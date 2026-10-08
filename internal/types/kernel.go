// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/naming"
)

// The CPU architectures a kernel may be built for, as uname -m names them.
const (
	ArchitectureX86_64  = "x86_64"
	ArchitectureAArch64 = "aarch64"
)

// DefaultKernelName is the name of the default kernel, which the daemon
// defines itself and an instance boots when it names no kernel.
const DefaultKernelName = "default"

// Kernel is a guest kernel image an instance can boot from, kept on the
// host.
type Kernel struct {
	ID           string    `yaml:"id"`
	Name         string    `yaml:"name"`
	Architecture string    `yaml:"arch"`
	SHA256       string    `yaml:"sha256"`
	CreatedAt    time.Time `yaml:"created_at"`
	UpdatedAt    time.Time `yaml:"updated_at"`
}

// Validate returns an invalid argument error unless the kernel has a valid
// name and an architecture Dicer knows, and its checksum, if it has one, is a
// hex-encoded SHA-256 digest.
func (k Kernel) Validate() error {
	if err := naming.Validate(k.Name); err != nil {
		return err
	}

	switch {
	case k.Architecture == "":
		return errdefs.InvalidArgument("a kernel needs an architecture: %s or %s",
			ArchitectureX86_64, ArchitectureAArch64)
	case k.Architecture != ArchitectureX86_64 && k.Architecture != ArchitectureAArch64:
		return errdefs.InvalidArgument("unknown architecture %q: want %s or %s",
			k.Architecture, ArchitectureX86_64, ArchitectureAArch64)
	case k.SHA256 != "" && !isSHA256Hex(k.SHA256):
		return errdefs.InvalidArgument("sha256 %q is not a hex-encoded SHA-256 digest", k.SHA256)
	}
	return nil
}

// isSHA256Hex reports whether s is a hex-encoded SHA-256 digest.
func isSHA256Hex(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size
}
