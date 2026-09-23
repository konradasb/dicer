// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import "github.com/prometheus/client_golang/prometheus"

// NetworkStats summarises one network's address pool.
type NetworkStats struct {
	Name string

	// Allocated is how many addresses the network has handed out.
	Allocated int

	// Available is how many assignable addresses remain. The network,
	// broadcast and gateway addresses are not assignable and are excluded.
	Available int64
}

// networkCollector exports address pool usage per network.
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
