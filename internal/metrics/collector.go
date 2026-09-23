// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import "github.com/prometheus/client_golang/prometheus"

// The gauges describing what currently exists -- instances by state, images
// held -- are collected when a scrape arrives rather than maintained as
// things change. That is deliberate: the daemon is not the only thing that
// decides whether an instance is running. A VMM can die on its own, and
// recovery re-adopts whatever it finds at startup, so a hand-maintained gauge
// would be a second source of truth that quietly disagrees with the first.
// Reading the real state per scrape cannot drift.
//
// Each is a prometheus.Collector rather than a set of prometheus.GaugeFunc
// because every gauge it exports comes from one call to its source. Several
// GaugeFuncs would call that source once each, and could report an instance
// count and a vCPU total taken from different moments.

// desc builds a metric descriptor in this daemon's namespace.
func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(prometheus.BuildFQName(namespace, "", name), help, labels, nil)
}

// gauge builds one gauge reading for a descriptor. The value is read from a
// source this package owns, so a label mismatch is a programming error and
// the panic is the report.
func gauge(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}
