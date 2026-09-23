// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// Pause halts the guest's vCPUs without tearing anything down.
func (m *Manager) Pause(ctx context.Context, inst types.InstanceSpec) error {
	return m.setPaused(ctx, inst, pauseMove{
		op:      opPause,
		from:    types.StateRunning,
		to:      types.StatePaused,
		do:      hypervisor.Hypervisor.PauseVM,
		event:   types.ActionPaused,
		message: "Paused instance: vCPUs halted, memory kept",
	})
}

// Resume restarts the vCPUs of a paused instance.
func (m *Manager) Resume(ctx context.Context, inst types.InstanceSpec) error {
	return m.setPaused(ctx, inst, pauseMove{
		op:      opResume,
		from:    types.StatePaused,
		to:      types.StateRunning,
		do:      hypervisor.Hypervisor.ResumeVM,
		event:   types.ActionResumed,
		message: "Resumed instance: vCPUs running",
	})
}

// pauseMove describes a pause or resume.
type pauseMove struct {
	op       string
	from, to types.InstanceState
	do       func(hypervisor.Hypervisor, context.Context) error
	event    types.EventAction
	message  string
}

// setPaused moves a live instance between running and paused.
func (m *Manager) setPaused(ctx context.Context, inst types.InstanceSpec, mv pauseMove) (err error) {
	started := time.Now()
	defer func() { m.observe(mv.op, started, err) }()

	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	rt, err := m.Runtime(inst)
	if err != nil {
		return err
	}
	if rt.State != mv.from {
		return errdefs.InvalidState("instance %q is %s, not %s", inst.Name, rt.State.Lower(), mv.from.Lower())
	}

	hv, err := m.connect(inst, rt)
	if err != nil {
		return err
	}
	if err := requireCapability(inst, hv.Capabilities().SupportsPause, mv.op); err != nil {
		return err
	}

	if err := mv.do(hv, ctx); err != nil {
		return fmt.Errorf("%s vm: %w", mv.op, err)
	}
	if err := m.transition(inst, mv.to); err != nil {
		return err
	}

	m.record(inst, mv.event, mv.message, nil)
	m.logger.InfoContext(ctx, "changed instance state", "instance", inst.Name, "state", mv.to.Lower())
	return nil
}
