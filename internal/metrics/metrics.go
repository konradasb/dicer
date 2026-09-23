// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package metrics exports dicerd's Prometheus metrics. Counters and
// histograms are recorded through the Record methods; state gauges are read
// from Sources at scrape time.
package metrics

import (
	"log/slog"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// namespace prefixes every metric this daemon exports.
const namespace = "dicer"

// Outcome label values, distinguishing a successful operation from a failed
// one. They are derived from an error rather than passed by the caller, so a
// failure cannot be reported as a success by mistake.
const (
	outcomeSuccess = "success"
	outcomeError   = "error"
)

// Sources are read at scrape time to fill the state gauges. A nil source's
// gauges are not exported.
type Sources struct {
	Instances func() InstanceStats
	Networks  func() []NetworkStats
	Images    func() ImageStats
}

// Options configures a Metrics.
type Options struct {
	// Version and Commit identify the build, reported as labels on
	// dicer_build_info.
	Version string
	Commit  string

	// Sources feed the scrape-time gauges.
	Sources Sources

	// Logger receives errors encountered while serving a scrape. Defaults
	// to slog.Default.
	Logger *slog.Logger
}

// Metrics holds the registry and every metric dicerd exports, grouped by
// what they measure.
type Metrics struct {
	registry *prometheus.Registry
	logger   *slog.Logger

	instance instanceMetrics
	grpc     grpcMetrics
	image    imageMetrics
}

// New creates the registry and registers every metric on it.
func New(opts Options) *Metrics {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	m := &Metrics{
		registry: prometheus.NewRegistry(),
		logger:   logger,
		instance: newInstanceMetrics(),
		grpc:     newGRPCMetrics(),
		image:    newImageMetrics(),
	}

	m.register(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo(opts.Version, opts.Commit),
		processStartTime(),
	)
	m.register(m.instance.collectors()...)
	m.register(m.grpc.collectors()...)
	m.register(m.image.collectors()...)

	if src := opts.Sources.Instances; src != nil {
		m.register(newInstanceCollector(src))
	}
	if src := opts.Sources.Networks; src != nil {
		m.register(newNetworkCollector(src))
	}
	if src := opts.Sources.Images; src != nil {
		m.register(newImageCollector(src))
	}

	return m
}

// register adds collectors to the registry. Every caller is this package
// registering a metric it just built, so a duplicate is a programming error
// and panicking at startup is the right response.
func (m *Metrics) register(cs ...prometheus.Collector) {
	m.registry.MustRegister(cs...)
}

// buildInfo is the conventional info metric: always 1, carrying the build in
// its labels so a dashboard can join on it or alert on a version change.
func buildInfo(version, commit string) prometheus.Collector {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "Build information for the running daemon. Always 1.",
	}, []string{"version", "commit", "go_version"})

	g.WithLabelValues(version, commit, runtime.Version()).Set(1)

	return g
}

// processStartTime reports when this process started, as a Unix timestamp.
// Uptime is then `time() - dicer_start_time_seconds`, which stays correct
// across a scrape gap in a way a self-counting uptime gauge does not.
func processStartTime() prometheus.Collector {
	started := float64(time.Now().Unix())

	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "start_time_seconds",
		Help:      "Start time of the daemon since the Unix epoch, in seconds.",
	}, func() float64 { return started })
}

// outcome maps an error to the label value describing it.
func outcome(err error) string {
	if err != nil {
		return outcomeError
	}
	return outcomeSuccess
}
