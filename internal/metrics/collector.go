// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import "github.com/prometheus/client_golang/prometheus"

// State gauges, and counters kept by something else, are read from their
// source at scrape time. Each collector reads its source once per scrape so
// its metrics are consistent.

// descriptor builds the descriptor of a metric read at scrape time,
// described by d, and adds d to the reference. d.Type is gauge unless set.
// The collector holding it is registered by its caller.
func (m *Metrics) descriptor(d Description) *prometheus.Desc {
	if d.Type == "" {
		d.Type = "gauge"
	}
	m.reference = append(m.reference, d)

	return prometheus.NewDesc(d.Name, d.Help, d.Labels, nil)
}

// gaugeReading builds one gauge reading for a descriptor. The value is read
// from a source this package owns, so a label mismatch is a programming
// error and the panic is the report.
func gaugeReading(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}

// counterReading builds one counter reading for a descriptor, as
// gaugeReading does.
func counterReading(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, labels...)
}
