// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
)

// TestIdleTrackerCountsOnlyAnUnbrokenIdleSpell checks that an instance is
// idle for as long as every span between samples was, and that one busy
// span, or another run of it, starts the count again.
func TestIdleTrackerCountsOnlyAnUnbrokenIdleSpell(t *testing.T) {
	started := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	sample := func(minute int, cpu time.Duration, packets int64) types.InstanceStats {
		return types.InstanceStats{
			InstanceID: "web", StartedAt: started,
			ReadAt:  started.Add(time.Duration(minute) * time.Minute),
			CPUTime: cpu, NetworkReceivePackets: packets,
		}
	}

	tests := []struct {
		name    string
		samples []types.InstanceStats
		want    time.Duration
	}{
		{"first sample", []types.InstanceStats{sample(0, 0, 0)}, 0},
		{"idle throughout", []types.InstanceStats{
			sample(0, 0, 0), sample(1, time.Second, 10), sample(2, 2*time.Second, 20), sample(3, 3*time.Second, 30),
		}, 3 * time.Minute},
		{"busy CPU last minute", []types.InstanceStats{
			sample(0, 0, 0), sample(1, time.Second, 0), sample(2, 10*time.Second, 0),
		}, 0},
		{"busy network, then idle", []types.InstanceStats{
			sample(0, 0, 0), sample(1, 0, 600), sample(2, 0, 610), sample(3, 0, 620),
		}, 2 * time.Minute},
		{"restarted", []types.InstanceStats{
			sample(0, 0, 0), sample(1, 0, 0),
			{InstanceID: "web", StartedAt: started.Add(90 * time.Second), ReadAt: started.Add(2 * time.Minute)},
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tracker idleTracker
			var got time.Duration
			for _, s := range tt.samples {
				got = tracker.observe(s)
			}
			if got != tt.want {
				t.Errorf("idle for %s, want %s", got, tt.want)
			}
		})
	}
}

// TestIdleInstanceIsPutOnStandby checks that an instance idle for its
// standby_after goes on standby, and that one that has not opted in, or is
// paused, does not.
func TestIdleInstanceIsPutOnStandby(t *testing.T) {
	tests := []struct {
		name         string
		standbyAfter time.Duration
		paused       bool
		want         types.InstanceState
	}{
		{"idle long enough", 2 * time.Minute, false, types.InstanceStateStandby},
		{"not idle long enough", 10 * time.Minute, false, types.InstanceStateRunning},
		{"never", 0, false, types.InstanceStateRunning},
		{"paused", 2 * time.Minute, true, types.InstanceStatePaused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.instance.StandbyAfter = tt.standbyAfter
			h.definitions.instances[h.instance.Name] = h.instance
			h.start(t)
			if tt.paused {
				if err := h.manager.Pause(t.Context(), h.instance); err != nil {
					t.Fatal(err)
				}
			}
			startedAt := h.status(t).StartedAt

			var tracker idleTracker
			for minute := range 4 {
				h.manager.standbyIdle(t.Context(), &tracker, []types.InstanceStats{{
					InstanceID: h.instance.ID, StartedAt: startedAt,
					ReadAt: startedAt.Add(time.Duration(minute) * time.Minute),
				}})
			}

			if got := h.status(t).State; got != tt.want {
				t.Errorf("state = %s, want %s", got, tt.want)
			}
			if e, ok := h.events.last(events.ActionStandby); tt.want == types.InstanceStateStandby &&
				(!ok || e.Attributes["idle_seconds"] != "120") {
				t.Errorf("standby event = %+v, want one saying it was idle 120s", e)
			}
		})
	}
}
