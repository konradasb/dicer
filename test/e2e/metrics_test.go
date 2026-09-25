// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strconv"
	"strings"
	"testing"
)

// TestMetricsReportWhatIsRunning scrapes the daemon and checks the gauges
// against instances that really exist.
//
// The unit tests prove the registry reports whatever its sources say. Only a
// real host can show that the sources say something true: that a booted VM
// appears as Running, and that its address is counted against the pool.
func TestMetricsReportWhatIsRunning(t *testing.T) {
	name := instanceName(t)

	before := env.gauge(t, `dicer_instances{state="running"}`)
	allocatedBefore := env.gauge(t, `dicer_network_addresses_allocated{network="`+networkName+`"}`)

	env.createInstance(t, name)
	env.startInstance(t, name)

	if got := env.gauge(t, `dicer_instances{state="running"}`); got != before+1 {
		t.Errorf("running instances = %v after starting one, want %v", got, before+1)
	}
	if got := env.gauge(t, `dicer_network_addresses_allocated{network="`+networkName+`"}`); got != allocatedBefore+1 {
		t.Errorf("allocated addresses = %v, want %v", got, allocatedBefore+1)
	}

	// A running guest has its memory committed on the host, so the gauge
	// has to be at least the one instance's worth.
	if got := env.gauge(t, "dicer_instances_memory_bytes"); got < 512<<20 {
		t.Errorf("committed memory = %v, want at least the running instance's 512MiB", got)
	}

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")

	if got := env.gauge(t, `dicer_instances{state="running"}`); got != before {
		t.Errorf("running instances = %v after stopping it again, want %v", got, before)
	}

	// The address outlives the stop, and the gauge has to agree with the
	// address manager rather than with the instance's state.
	if got := env.gauge(t, `dicer_network_addresses_allocated{network="`+networkName+`"}`); got != allocatedBefore+1 {
		t.Errorf("allocated addresses = %v after a stop, want %v", got, allocatedBefore+1)
	}
}

// TestMetricsCountLifecycleOperations checks that the operation counter moves
// with real operations, including the ones that fail.
func TestMetricsCountLifecycleOperations(t *testing.T) {
	name := instanceName(t)

	const (
		starts = `dicer_instance_operations_total{operation="start",outcome="success"}`
		failed = `dicer_instance_operations_total{operation="pause",outcome="error"}`
	)

	startsBefore := env.gauge(t, starts)
	failedBefore := env.gauge(t, failed)

	env.createInstance(t, name)

	// Pausing an instance that is not running fails in the daemon, before
	// it touches the host. It should still be counted.
	if _, err := env.tryDicer(t, "instance", "pause", name); err == nil {
		t.Fatal("pausing a stopped instance should fail")
	}
	if got := env.gauge(t, failed); got != failedBefore+1 {
		t.Errorf("failed pauses = %v, want %v", got, failedBefore+1)
	}

	env.startInstance(t, name)

	if got := env.gauge(t, starts); got != startsBefore+1 {
		t.Errorf("successful starts = %v, want %v", got, startsBefore+1)
	}
}

// gauge scrapes the metrics endpoint and returns one series' value. A series
// that is absent reads as zero, which is what a counter that has never been
// incremented means.
func (e *environment) gauge(t *testing.T, series string) float64 {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	// curl runs on the host because the endpoint is bound to its loopback
	// address, which is the configuration an operator would use.
	body, err := e.host.run(ctx, "curl", "-sf", "http://"+e.paths.metrics+"/metrics")
	if err != nil {
		t.Fatalf("scrape %s: %v", e.paths.metrics, err)
	}

	for line := range strings.Lines(body) {
		name, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || name != series {
			continue
		}

		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("parse %s from %q: %v", series, line, err)
		}

		return parsed
	}

	return 0
}
