// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import "time"

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
