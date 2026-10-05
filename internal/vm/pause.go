// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// Pause halts the guest's vCPUs without tearing anything down.
func (m *Manager) Pause(ctx context.Context, instance types.InstanceSpec) error {
	return m.setPaused(ctx, instance, pauseMove{
		operation: operationPause,
		from:      types.InstanceStateRunning,
		to:        types.InstanceStatePaused,
		do:        hypervisor.Hypervisor.PauseVM,
		event:     events.ActionPaused,
		message:   "Paused instance: vCPUs halted, memory kept",
	})
}

// Resume restarts the vCPUs of a paused instance.
func (m *Manager) Resume(ctx context.Context, instance types.InstanceSpec) error {
	return m.setPaused(ctx, instance, pauseMove{
		operation: operationResume,
		from:      types.InstanceStatePaused,
		to:        types.InstanceStateRunning,
		do:        hypervisor.Hypervisor.ResumeVM,
		event:     events.ActionResumed,
		message:   "Resumed instance: vCPUs running",
	})
}

// pauseMove describes a pause or resume.
type pauseMove struct {
	operation string
	from, to  types.InstanceState
	do        func(hypervisor.Hypervisor, context.Context) error
	event     events.Action
	message   string
}

// setPaused moves a live instance between running and paused.
func (m *Manager) setPaused(ctx context.Context, instance types.InstanceSpec, move pauseMove) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(move.operation, started, err) }()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := m.Status(instance)
	if err != nil {
		return err
	}
	if status.State != move.from {
		return errdefs.InvalidState("instance %q is %s, not %s", instance.Name, status.State.Lowercase(), move.from.Lowercase())
	}

	hv, err := m.connect(instance, status)
	if err != nil {
		return err
	}
	if err := requireCapability(instance, hv.Capabilities().SupportsPause, move.operation); err != nil {
		return err
	}

	if err := move.do(hv, ctx); err != nil {
		return fmt.Errorf("%s vm: %w", move.operation, err)
	}
	if err := m.transition(instance, move.to); err != nil {
		return err
	}

	m.record(instance, move.event, move.message, nil)
	m.logger.InfoContext(ctx, "changed instance state", "instance", instance.Name, "state", move.to.Lowercase())
	return nil
}
