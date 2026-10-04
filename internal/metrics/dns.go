// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// dnsMetrics measures the DNS servers that answer each network's guests.
type dnsMetrics struct {
	queries         *prometheus.CounterVec
	forwardDuration *prometheus.HistogramVec
}

func (m *Metrics) newDNSMetrics() dnsMetrics {
	return dnsMetrics{
		queries: m.counterVec(Description{
			Name:   "dicer_dns_queries_total",
			Labels: []string{"network", "result"},
			Help:   "DNS queries guests sent a network's server, by how they were answered.",
			Doc: "`result` is `local`, from the network's own names, found or not; `forwarded`, " +
				"answered by an upstream nameserver; `failed`, by none, with SERVFAIL; `invalid`, " +
				"not one query; or `dropped`, for the server being too busy. A TCP connection closed " +
				"for that counts as one query dropped.",
			Group: GroupDNS,
		}),

		forwardDuration: m.histogramVec(Description{
			Name:   "dicer_dns_forward_duration_seconds",
			Labels: []string{"network"},
			Help:   "Time asking a network's upstream nameservers for an answer took, answered or not.",
			Doc:    "Each is asked in turn until one answers, for up to 3 seconds each.",
			Group:  GroupDNS,
		}, prometheus.ExponentialBuckets(0.001, 2, 14)),
	}
}

// RecordDNSQuery records a query to a network's DNS server, and how it was
// answered.
func (m *Metrics) RecordDNSQuery(network, result string) {
	m.dns.queries.WithLabelValues(network, result).Inc()
}

// RecordDNSForward records how long asking a network's upstream
// nameservers took.
func (m *Metrics) RecordDNSForward(network string, d time.Duration) {
	m.dns.forwardDuration.WithLabelValues(network).Observe(d.Seconds())
}
