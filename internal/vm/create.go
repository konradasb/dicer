// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/image/reference"
)

// Create records a new instance's definition. It boots nothing: see Start.
func (m *Manager) Create(_ context.Context, inst dicer.InstanceSpec) error {
	if err := m.definitions.CreateInstance(inst); err != nil {
		return err
	}
	m.record(inst, dicer.ActionCreated,
		fmt.Sprintf("Created instance from image %s with %s, %s memory, %s disk; restart policy %s",
			reference.Familiar(inst.ImageRef), vcpus(inst.VCPUs), size(inst.MemoryBytes), size(inst.DiskBytes),
			inst.Restart),
		map[string]string{"image": inst.ImageRef})
	return nil
}
