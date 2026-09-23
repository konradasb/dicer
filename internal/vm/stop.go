// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/dicer-sh/dicer"
)

// Stop shuts down a running instance and releases the host resources it
// holds. The definition, its overlay disk and its address allocation are
// kept, so the instance can be started again and comes back the same.
//
// Stopping an already-stopped instance is not an error: the caller asked for
// it to not be running, and it is not running.
//
// A stop is the user taking the instance over: a restart it was waiting on
// is cancelled, and it is recorded as stopped by a user, which keeps an
// unless-stopped instance down when the daemon next starts.
func (m *Manager) Stop(ctx context.Context, inst dicer.InstanceSpec) (err error) {
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
	if rt.State == dicer.StateStopped {
		return nil
	}

	if err := m.transition(inst, dicer.StateStopping); err != nil {
		return err
	}
	// Once Stopping, the stop is seen through whatever becomes of the
	// request: a client that gives up must not leave the instance's host
	// network half torn down. stopVMM bounds how long it takes.
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
	m.record(inst, dicer.ActionStopped, stopMessage(outcome, m.stopGracePeriod, took, ranFor), nil)
	m.logger.InfoContext(ctx, "stopped instance", "instance", inst.Name)

	// A stop is a stop, however it was asked for: an instance that asked to
	// be deleted when it stops is deleted here too, as docker run --rm is.
	m.scheduleRemoval(ctx, inst)

	return nil
}
