// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import "github.com/prometheus/client_golang/prometheus"

// NetworkStats is a point-in-time summary of one network's address pool.
//
// Allocated and Available are reported as two series rather than a usage
// ratio, because a ratio cannot be summed, and because the useful alert is
// written against both: a /24 that is 90% full is urgent, a /16 at 90% is a
// different conversation.
type NetworkStats struct {
	Name string

	// Allocated is how many addresses the network has handed out.
	Allocated int

	// Available is how many assignable addresses remain. The network,
	// broadcast and gateway addresses are not assignable and are excluded.
	Available int64
}

// networkCollector exports address pool usage per network. See collector.go
// for why this is read per scrape.
//
// This is the one resource on the host with a hard ceiling that the daemon
// cannot work around: when a subnet fills, every subsequent instance start
// fails, and nothing before that point says it is coming.
type networkCollector struct {
	source func() []NetworkStats

	allocated *prometheus.Desc
	available *prometheus.Desc
}

func newNetworkCollector(source func() []NetworkStats) *networkCollector {
	return &networkCollector{
		source: source,
		allocated: desc("network_addresses_allocated",
			"Addresses currently assigned to instances on a network.", "network"),
		available: desc("network_addresses_available",
			"Assignable addresses still free on a network.", "network"),
	}
}

func (c *networkCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.allocated
	ch <- c.available
}

func (c *networkCollector) Collect(ch chan<- prometheus.Metric) {
	for _, nw := range c.source() {
		ch <- gauge(c.allocated, float64(nw.Allocated), nw.Name)
		ch <- gauge(c.available, float64(nw.Available), nw.Name)
	}
}
