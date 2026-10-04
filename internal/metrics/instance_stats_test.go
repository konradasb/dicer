// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

func TestInstanceStatsAreReadPerScrape(t *testing.T) {
	stats := []types.InstanceStats{{
		InstanceID:             "i-web",
		Name:                   "web",
		CPUTime:                1500 * time.Millisecond,
		ResidentMemoryBytes:    1 << 20,
		DiskReadBytes:          10,
		DiskWrittenBytes:       20,
		Committed:              types.Resources{VCPUs: 2, MemoryBytes: 1 << 30},
		NetworkReceiveBytes:    30,
		NetworkTransmitBytes:   40,
		NetworkReceivePackets:  3,
		NetworkTransmitPackets: 4,
		NetworkReceiveDrops:    5,
		NetworkTransmitDrops:   6,
		NetworkReceiveErrors:   7,
		NetworkTransmitErrors:  8,
	}}
	reads := 0

	m := New(Options{Sources: Sources{
		InstanceStats: func() []types.InstanceStats {
			reads++
			return stats
		},
	}})

	body := scrape(t, m)
	for _, want := range []string{
		"# TYPE dicer_instance_cpu_seconds_total counter",
		`dicer_instance_cpu_seconds_total{instance_id="i-web",name="web"} 1.5`,
		"# TYPE dicer_instance_resident_memory_bytes gauge",
		`dicer_instance_resident_memory_bytes{instance_id="i-web",name="web"} 1.048576e+06`,
		`dicer_instance_disk_read_bytes_total{instance_id="i-web",name="web"} 10`,
		`dicer_instance_disk_written_bytes_total{instance_id="i-web",name="web"} 20`,
		`dicer_instance_network_receive_bytes_total{instance_id="i-web",name="web"} 30`,
		`dicer_instance_network_transmit_bytes_total{instance_id="i-web",name="web"} 40`,
		`dicer_instance_vcpus{instance_id="i-web",name="web"} 2`,
		`dicer_instance_memory_bytes{instance_id="i-web",name="web"} 1.073741824e+09`,
		`dicer_instance_network_receive_packets_total{instance_id="i-web",name="web"} 3`,
		`dicer_instance_network_transmit_packets_total{instance_id="i-web",name="web"} 4`,
		`dicer_instance_network_receive_drops_total{instance_id="i-web",name="web"} 5`,
		`dicer_instance_network_transmit_drops_total{instance_id="i-web",name="web"} 6`,
		`dicer_instance_network_receive_errors_total{instance_id="i-web",name="web"} 7`,
		`dicer_instance_network_transmit_errors_total{instance_id="i-web",name="web"} 8`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %q:\n%s", want, body)
		}
	}
	if reads != 1 {
		t.Errorf("source called %d times for one scrape, want 1", reads)
	}

	// A stopped instance has no series rather than a stale one.
	stats = nil
	if body := scrape(t, m); strings.Contains(body, `name="web"`) {
		t.Errorf("scrape still has the instance once it stopped:\n%s", body)
	}
}

// TestInstanceStatsTotalsAreCounters checks that what only grows while an
// instance runs is a counter, and what is resident or committed, a gauge.
func TestInstanceStatsTotalsAreCounters(t *testing.T) {
	m := New(Options{Sources: Sources{InstanceStats: func() []types.InstanceStats { return nil }}})

	byName := map[string]string{}
	for _, d := range m.Reference() {
		byName[d.Name] = d.Type
	}
	for name, want := range map[string]string{
		"dicer_instance_cpu_seconds_total":              "counter",
		"dicer_instance_resident_memory_bytes":          "gauge",
		"dicer_instance_disk_read_bytes_total":          "counter",
		"dicer_instance_disk_written_bytes_total":       "counter",
		"dicer_instance_network_receive_bytes_total":    "counter",
		"dicer_instance_network_transmit_bytes_total":   "counter",
		"dicer_instance_vcpus":                          "gauge",
		"dicer_instance_memory_bytes":                   "gauge",
		"dicer_instance_network_receive_packets_total":  "counter",
		"dicer_instance_network_transmit_packets_total": "counter",
		"dicer_instance_network_receive_drops_total":    "counter",
		"dicer_instance_network_transmit_drops_total":   "counter",
		"dicer_instance_network_receive_errors_total":   "counter",
		"dicer_instance_network_transmit_errors_total":  "counter",
	} {
		if got := byName[name]; got != want {
			t.Errorf("reference lists %s as %q, want %q", name, got, want)
		}
	}
}
