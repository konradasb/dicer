// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import "github.com/prometheus/client_golang/prometheus"

// State gauges are read from their source at scrape time. Each collector
// reads its source once per scrape so its gauges are consistent.

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
