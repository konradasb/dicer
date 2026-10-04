// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/types"
)

// Create records a new instance's definition, first pulling its image as
// pull says. It boots nothing: see Start. Nothing is recorded if the image
// cannot be had.
func (m *Manager) Create(ctx context.Context, inst types.InstanceSpec, pull types.PullPolicy) error {
	// Before the definition, so that one whose image cannot be had is never
	// seen, even for as long as a pull takes. The error names the image.
	if _, err := m.images.Ensure(ctx, inst.ImageRef, pull); err != nil {
		return err
	}

	if err := m.definitions.CreateInstance(inst); err != nil {
		return err
	}
	m.record(inst, events.ActionCreated,
		fmt.Sprintf("Created instance from image %s with %s, %s memory, %s disk; restart policy %s",
			reference.Familiar(inst.ImageRef), humanize.Count(inst.VCPUs, "vCPU"), humanize.Bytes(inst.MemoryBytes), humanize.Bytes(inst.DiskBytes),
			inst.Restart),
		map[string]string{"image": inst.ImageRef})
	return nil
}
