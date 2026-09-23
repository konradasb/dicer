// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"strings"
	"testing"
)

func TestNetworkGaugesAreReadPerScrape(t *testing.T) {
	stats := []NetworkStats{
		{Name: "default", Allocated: 41, Available: 212},
		{Name: "isolated", Allocated: 0, Available: 13},
	}
	reads := 0

	m := New(Options{Sources: Sources{
		Networks: func() []NetworkStats {
			reads++
			return stats
		},
	}})

	body := scrape(t, m)
	for _, want := range []string{
		`dicer_network_addresses_allocated{network="default"} 41`,
		`dicer_network_addresses_available{network="default"} 212`,
		`dicer_network_addresses_allocated{network="isolated"} 0`,
		`dicer_network_addresses_available{network="isolated"} 13`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %q:\n%s", want, body)
		}
	}
	if reads != 1 {
		t.Errorf("source called %d times for one scrape, want 1", reads)
	}

	// A filling subnet is the condition this exists to catch, so the gauges
	// have to move with it rather than be sampled once at startup.
	stats[0] = NetworkStats{Name: "default", Allocated: 253, Available: 0}
	if body := scrape(t, m); !strings.Contains(body, `dicer_network_addresses_available{network="default"} 0`) {
		t.Errorf("second scrape did not pick up the exhausted pool:\n%s", body)
	}
}

// A host can define no networks at all, which must scrape cleanly rather
// than fail or invent a series.
func TestNetworkGaugesWithNoNetworks(t *testing.T) {
	m := New(Options{Sources: Sources{
		Networks: func() []NetworkStats { return nil },
	}})

	if body := scrape(t, m); strings.Contains(body, "dicer_network_addresses") {
		t.Errorf("scrape invented a series with no networks defined:\n%s", body)
	}
}
