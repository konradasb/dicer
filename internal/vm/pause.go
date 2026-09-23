// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/hypervisor"
)

// Pause halts the guest's vCPUs without tearing anything down.
func (m *Manager) Pause(ctx context.Context, inst dicer.InstanceSpec) error {
	return m.setPaused(ctx, inst, pauseMove{
		op:      opPause,
		from:    dicer.StateRunning,
		to:      dicer.StatePaused,
		do:      hypervisor.Hypervisor.PauseVM,
		event:   dicer.ActionPaused,
		message: "Paused instance: vCPUs halted, memory kept",
	})
}

// Resume restarts the vCPUs of a paused instance.
func (m *Manager) Resume(ctx context.Context, inst dicer.InstanceSpec) error {
	return m.setPaused(ctx, inst, pauseMove{
		op:      opResume,
		from:    dicer.StatePaused,
		to:      dicer.StateRunning,
		do:      hypervisor.Hypervisor.ResumeVM,
		event:   dicer.ActionResumed,
		message: "Resumed instance: vCPUs running",
	})
}

// pauseMove is one direction of pausing: the operation's name, the state it
// leaves and enters, and the hypervisor call that does it.
type pauseMove struct {
	op       string
	from, to dicer.InstanceState
	do       func(hypervisor.Hypervisor, context.Context) error
	event    dicer.EventAction
	message  string
}

// setPaused moves a live instance between running and paused.
func (m *Manager) setPaused(ctx context.Context, inst dicer.InstanceSpec, mv pauseMove) (err error) {
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
		return dicer.InvalidState("instance %q is %s, not %s", inst.Name, rt.State.Lower(), mv.from.Lower())
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
