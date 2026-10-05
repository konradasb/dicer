// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// idleSampleInterval is how often running instances are sampled to judge
// them idle: each span between two samples is idle or busy as a whole.
const idleSampleInterval = time.Minute

// A span is idle while the guest uses under idleCPUFraction of one vCPU and
// its network carries under idlePacketsPerSecond, which background chatter,
// NTP or DNS, stays below.
const (
	idleCPUFraction      = 0.05
	idlePacketsPerSecond = 1
)

// StandbyIdle puts on standby each running instance that has been idle for
// its StandbyAfter, sampling them every idleSampleInterval until ctx is
// done.
func (m *Manager) StandbyIdle(ctx context.Context) {
	tracker := idleTracker{}
	ticker := time.NewTicker(idleSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.standbyIdle(ctx, &tracker, m.Stats())
		}
	}
}

// standbyIdle judges each running instance that can go on standby idle or
// busy from a sample of its stats, and puts on standby those idle for long
// enough.
func (m *Manager) standbyIdle(ctx context.Context, tracker *idleTracker, sample []types.InstanceStats) {
	sampled := make(map[string]bool, len(sample))
	for _, stats := range sample {
		instance, err := m.definitions.Instance(stats.InstanceID)
		if err != nil || instance.StandbyAfter == 0 {
			continue
		}
		// A paused instance is not idle but stopped by a user, and its
		// pause would count against it once it is resumed.
		if status, err := m.Status(instance); err != nil || status.State != types.InstanceStateRunning {
			continue
		}

		sampled[instance.ID] = true
		idleFor := tracker.observe(stats)
		if idleFor < instance.StandbyAfter {
			continue
		}
		if err := m.standby(ctx, instance, idleFor); err != nil {
			m.logger.WarnContext(ctx, "cannot put an idle instance on standby", "instance", instance.Name, "error", err)
		}
	}
	tracker.keepOnly(sampled)
}

// idleTracker follows how long instances have been idle, from successive
// samples of their stats. The zero value is ready to use. It is not safe
// for concurrent use.
type idleTracker struct {
	instances map[string]idleSamples
}

// idleSamples is an instance's last sample, and since when it has been
// idle; zero while it is busy.
type idleSamples struct {
	last      types.InstanceStats
	idleSince time.Time
}

// observe takes a sample of an instance's stats and returns how long it has
// been idle: zero if it was busy since the last sample, or if this is the
// first of its run.
func (t *idleTracker) observe(stats types.InstanceStats) time.Duration {
	if t.instances == nil {
		t.instances = make(map[string]idleSamples)
	}

	prev, ok := t.instances[stats.InstanceID]
	next := idleSamples{last: stats}
	// A sample of another run of the instance says nothing of this one.
	if ok && prev.last.StartedAt.Equal(stats.StartedAt) && idle(prev.last, stats) {
		next.idleSince = prev.idleSince
		if next.idleSince.IsZero() {
			next.idleSince = prev.last.ReadAt
		}
	}
	t.instances[stats.InstanceID] = next

	if next.idleSince.IsZero() {
		return 0
	}
	return stats.ReadAt.Sub(next.idleSince)
}

// keepOnly forgets every instance but those sampled.
func (t *idleTracker) keepOnly(sampled map[string]bool) {
	for id := range t.instances {
		if !sampled[id] {
			delete(t.instances, id)
		}
	}
}

// idle reports whether an instance was idle between two samples of its
// stats.
func idle(before, after types.InstanceStats) bool {
	span := after.ReadAt.Sub(before.ReadAt)
	if span <= 0 {
		return false
	}

	cpu := after.CPUTime - before.CPUTime
	packets := after.NetworkReceivePackets + after.NetworkTransmitPackets -
		before.NetworkReceivePackets - before.NetworkTransmitPackets

	return float64(cpu) < idleCPUFraction*float64(span) &&
		float64(packets) < idlePacketsPerSecond*span.Seconds()
}
