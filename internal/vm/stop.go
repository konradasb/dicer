// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// Stop shuts down an instance and releases its host resources, keeping its
// definition, disk and address. It cancels any pending restart and sets
// StoppedByUser. Stopping a stopped instance is not an error.
func (m *Manager) Stop(ctx context.Context, inst types.InstanceSpec) (err error) {
	started := time.Now()
	defer func() { m.observe(opStop, started, err) }()

	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	m.cancelRestart(inst.ID)
	m.setStoppedByUser(ctx, inst, true)

	rt, err := m.Runtime(inst)
	if err != nil {
		return err
	}
	if rt.State == types.StateStopped {
		return nil
	}

	if err := m.transition(inst, types.StateStopping); err != nil {
		return err
	}
	// Finish the stop even if the request is cancelled.
	ctx = context.WithoutCancel(ctx)

	stopping := time.Now()
	outcome := m.stopVMM(ctx, inst, rt, true)
	took := time.Since(stopping)
	m.teardownNetwork(ctx, inst)

	if err := m.clearRuntime(inst.ID); err != nil {
		return fmt.Errorf("clear runtime state: %w", err)
	}

	var ranFor time.Duration
	if !rt.StartedAt.IsZero() {
		ranFor = time.Since(rt.StartedAt)
	}
	m.record(inst, types.ActionStopped, stopMessage(outcome, m.stopGracePeriod, took, ranFor), nil)
	m.logger.InfoContext(ctx, "stopped instance", "instance", inst.Name)

	m.scheduleRemoval(ctx, inst)

	return nil
}
