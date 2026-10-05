// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
)

// Stop shuts down an instance and releases its host resources, keeping its
// definition, disk and address. An instance on standby is stopped by
// discarding what it has frozen, so that it boots afresh. It cancels any
// pending restart and sets StoppedByUser. Stopping a stopped instance is not
// an error.
func (m *Manager) Stop(ctx context.Context, instance types.InstanceSpec) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationStop, started, err) }()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	m.cancelRestart(instance.ID)
	m.setStoppedByUser(ctx, instance, true)

	status, err := m.Status(instance)
	if err != nil {
		return err
	}
	if err := m.discardStandby(instance); err != nil {
		return err
	}
	switch status.State {
	case types.InstanceStateStopped:
		return nil
	case types.InstanceStateStandby:
		m.record(instance, events.ActionStopped, "Stopped instance: discarded what it had frozen on standby", nil)
		m.logger.InfoContext(ctx, "stopped instance", "instance", instance.Name)
		m.scheduleRemoval(ctx, instance)
		return nil
	}

	if err := m.transition(instance, types.InstanceStateStopping); err != nil {
		return err
	}
	// Finish the stop even if the request is cancelled.
	ctx = context.WithoutCancel(ctx)

	stopping := time.Now()
	outcome := m.stopVMM(ctx, instance, status, true)
	took := time.Since(stopping)
	m.teardownNetwork(ctx, instance)

	if err := m.removeRuntimeDir(instance.ID); err != nil {
		return fmt.Errorf("remove runtime directory: %w", err)
	}

	var ranFor time.Duration
	if !status.StartedAt.IsZero() {
		ranFor = time.Since(status.StartedAt)
	}
	m.record(instance, events.ActionStopped, stopMessage(outcome, m.stopGracePeriod, took, ranFor), nil)
	m.logger.InfoContext(ctx, "stopped instance", "instance", instance.Name)

	m.scheduleRemoval(ctx, instance)

	return nil
}
