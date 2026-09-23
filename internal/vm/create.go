// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"

	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/types"
)

// Create records a new instance's definition. It boots nothing: see Start.
func (m *Manager) Create(_ context.Context, inst types.InstanceSpec) error {
	if err := m.definitions.CreateInstance(inst); err != nil {
		return err
	}
	m.record(inst, types.ActionCreated,
		fmt.Sprintf("Created instance from image %s with %s, %s memory, %s disk; restart policy %s",
			reference.Familiar(inst.ImageRef), vcpus(inst.VCPUs), size(inst.MemoryBytes), size(inst.DiskBytes),
			inst.Restart),
		map[string]string{"image": inst.ImageRef})
	return nil
}
