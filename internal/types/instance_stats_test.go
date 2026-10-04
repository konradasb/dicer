// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"testing"
	"time"
)

func TestCPUPercentIsMeasuredAgainstOneHostCPU(t *testing.T) {
	started := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	prev := InstanceStats{StartedAt: started, ReadAt: started.Add(time.Minute), CPUTime: 10 * time.Second}

	later := func(d time.Duration, cpu time.Duration) InstanceStats {
		return InstanceStats{StartedAt: started, ReadAt: prev.ReadAt.Add(d), CPUTime: prev.CPUTime + cpu}
	}

	tests := []struct {
		name          string
		prev, current InstanceStats
		want          float64
		wantOK        bool
	}{
		{name: "idle", prev: prev, current: later(time.Second, 0), want: 0, wantOK: true},
		{name: "half a CPU", prev: prev, current: later(2*time.Second, time.Second), want: 50, wantOK: true},
		{name: "two CPUs kept busy", prev: prev, current: later(time.Second, 2*time.Second), want: 200, wantOK: true},
		{
			// A restart begins the totals again, so they cannot be compared.
			name:    "another VMM",
			prev:    prev,
			current: InstanceStats{StartedAt: started.Add(time.Hour), ReadAt: prev.ReadAt.Add(time.Second)},
		},
		{name: "read at the same moment", prev: prev, current: later(0, 0)},
		{name: "never read before", prev: InstanceStats{}, current: later(time.Second, 0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.current.CPUPercent(tt.prev)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("CPUPercent() = %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
