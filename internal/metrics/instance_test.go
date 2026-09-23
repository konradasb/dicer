// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordInstanceOperationCountsByOutcome(t *testing.T) {
	m := New(Options{})

	m.RecordInstanceOperation("start", nil, 100*time.Millisecond)
	m.RecordInstanceOperation("start", nil, 200*time.Millisecond)
	m.RecordInstanceOperation("start", errors.New("boom"), 50*time.Millisecond)
	m.RecordInstanceOperation("stop", nil, 10*time.Millisecond)

	if got := testutil.ToFloat64(m.instance.operations.WithLabelValues("start", outcomeSuccess)); got != 2 {
		t.Errorf("start successes = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.instance.operations.WithLabelValues("start", outcomeError)); got != 1 {
		t.Errorf("start errors = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.instance.operations.WithLabelValues("stop", outcomeSuccess)); got != 1 {
		t.Errorf("stop successes = %v, want 1", got)
	}

	if got := testutil.CollectAndCount(m.instance.duration); got != 2 {
		t.Errorf("duration series = %d, want 2 (one per operation)", got)
	}
}

func TestRecordInstanceRestart(t *testing.T) {
	m := New(Options{})

	m.RecordInstanceRestart()
	m.RecordInstanceRestart()

	if got := testutil.ToFloat64(m.instance.restarts); got != 2 {
		t.Errorf("restarts = %v, want 2", got)
	}
}

func TestInstanceGaugesAreReadPerScrape(t *testing.T) {
	stats := InstanceStats{
		ByState:     map[string]int{"Running": 1, "Stopped": 0},
		ByHealth:    map[string]int{"healthy": 1, "unhealthy": 0},
		VCPUs:       2,
		MemoryBytes: 1 << 30,

		AllocatableVCPUs:       16,
		AllocatableMemoryBytes: 2 << 30,
	}
	reads := 0

	m := New(Options{Sources: Sources{
		Instances: func() InstanceStats {
			reads++
			return stats
		},
	}})

	body := scrape(t, m)
	for _, want := range []string{
		`dicer_instances{state="Running"} 1`,
		`dicer_instances{state="Stopped"} 0`,
		`dicer_instances_health{status="healthy"} 1`,
		`dicer_instances_health{status="unhealthy"} 0`,
		"dicer_instances_vcpus 2",
		"dicer_instances_memory_bytes 1.073741824e+09",
		"dicer_instances_vcpus_allocatable 16",
		"dicer_instances_memory_allocatable_bytes 2.147483648e+09",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %q:\n%s", want, body)
		}
	}

	// Every gauge in the group comes from one call, so a scrape cannot mix
	// readings taken at different moments.
	if reads != 1 {
		t.Errorf("source called %d times for one scrape, want 1", reads)
	}

	// The gauges follow the world rather than being set once at startup.
	stats.ByState["Running"] = 4
	if body := scrape(t, m); !strings.Contains(body, `dicer_instances{state="Running"} 4`) {
		t.Errorf("second scrape did not pick up the new value:\n%s", body)
	}
}
