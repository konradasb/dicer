// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import "time"

// Volume is persistent storage an instance can mount, outliving the
// instances that use it.
type Volume struct {
	ID        string    `yaml:"id"`
	Name      string    `yaml:"name"`
	Path      string    `yaml:"path"`
	SizeBytes int64     `yaml:"size_bytes"`
	CreatedAt time.Time `yaml:"created_at"`
	UpdatedAt time.Time `yaml:"updated_at"`
}
