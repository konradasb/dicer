// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import "time"

// The CPU architectures a kernel may be built for, as uname -m names them.
const (
	ArchX86_64  = "x86_64"
	ArchAArch64 = "aarch64"
)

// Kernel is a guest kernel image an instance can boot from, imported from a
// URL and kept on the host.
type Kernel struct {
	ID        string    `yaml:"id"`
	Name      string    `yaml:"name"`
	Arch      string    `yaml:"arch"`
	URL       string    `yaml:"url"`
	SHA256    string    `yaml:"sha256"`
	CreatedAt time.Time `yaml:"created_at"`
	UpdatedAt time.Time `yaml:"updated_at"`
}
