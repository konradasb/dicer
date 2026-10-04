// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import "github.com/prometheus/client_golang/prometheus"

// NetworkSummary summarises one network's address pool.
type NetworkSummary struct {
	Name string

	// Allocated is how many addresses the network has handed out.
	Allocated int

	// Available is how many assignable addresses remain. The network,
	// broadcast and gateway addresses are not assignable and are excluded.
	Available int64
}

// networkCollector exports address pool usage per network.
type networkCollector struct {
	source func() []NetworkSummary

	allocated *prometheus.Desc
	available *prometheus.Desc
}

func (m *Metrics) newNetworkCollector(source func() []NetworkSummary) *networkCollector {
	return &networkCollector{
		source: source,
		allocated: m.desc(Description{
			Name:   "dicer_network_addresses_allocated",
			Labels: []string{"network"},
			Help:   "Addresses currently assigned to instances on a network.",
			Group:  GroupNetworks,
		}),
		available: m.desc(Description{
			Name:   "dicer_network_addresses_available",
			Labels: []string{"network"},
			Help:   "Assignable addresses still free on a network.",
			Doc:    "The network, broadcast and gateway addresses are not counted.",
			Group:  GroupNetworks,
		}),
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
