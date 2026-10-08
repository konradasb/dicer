// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package metrics exports dicerd's Prometheus metrics. Counters and
// histograms are recorded through the Record methods; state gauges are read
// from Sources at scrape time.
package metrics

import (
	"log/slog"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/konradasb/dicer/internal/types"
)

// Metric groups: the sections of the reference.
const (
	GroupDaemon    = "daemon"
	GroupInstances = "instances"
	// GroupInstanceStats holds what each running instance uses of the host.
	GroupInstanceStats = "instance_stats"
	GroupImages        = "images"
	GroupKernels       = "kernels"
	GroupVolumes       = "volumes"
	GroupNetworks      = "networks"
	GroupDNS           = "dns"
	GroupAPI           = "api"
)

// Description describes one of Dicer's metrics. Every metric is built from
// one, so the generated reference lists exactly what is served.
type Description struct {
	// Name is the metric's full name.
	Name string

	// Type is counter, gauge or histogram.
	Type string

	// Labels are the metric's label names.
	Labels []string

	// Help is the metric's HELP: one plain sentence.
	Help string

	// Doc is what the reference adds to Help, in Markdown.
	Doc string

	// Group is the section of the reference the metric is listed in.
	Group string
}

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
	Instances     func() InstanceSummary
	InstanceStats func() []types.InstanceStats
	Networks      func() []NetworkSummary
	Images        func() ImageSummary
	Kernels       func() KernelSummary
	Volumes       func() VolumeSummary
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

	// reference is every metric of Dicer's own, in the order registered.
	reference []Description

	instance instanceMetrics
	grpc     grpcMetrics
	image    imageMetrics
	dns      dnsMetrics
}

// New creates the registry and registers every metric on it.
func New(options Options) *Metrics {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}

	m := &Metrics{registry: prometheus.NewRegistry(), logger: logger}

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m.registerBuildInfo(options.Version, options.Commit)

	if source := options.Sources.Instances; source != nil {
		m.registry.MustRegister(m.newInstanceCollector(source))
	}
	m.instance = m.newInstanceMetrics()

	if source := options.Sources.InstanceStats; source != nil {
		m.registry.MustRegister(m.newInstanceStatsCollector(source))
	}

	if source := options.Sources.Images; source != nil {
		m.registry.MustRegister(m.newImageCollector(source))
	}
	m.image = m.newImageMetrics()

	if source := options.Sources.Kernels; source != nil {
		m.registry.MustRegister(m.newKernelCollector(source))
	}

	if source := options.Sources.Volumes; source != nil {
		m.registry.MustRegister(m.newVolumeCollector(source))
	}

	if source := options.Sources.Networks; source != nil {
		m.registry.MustRegister(m.newNetworkCollector(source))
	}
	m.dns = m.newDNSMetrics()

	m.grpc = m.newGRPCMetrics()

	return m
}

// Reference returns every metric of Dicer's own this Metrics serves, in the
// order registered.
func (m *Metrics) Reference() []Description {
	return append([]Description(nil), m.reference...)
}

// registerBuildInfo registers the conventional info metric: always 1,
// carrying the build in its labels so a dashboard can join on it or alert on
// a version change.
func (m *Metrics) registerBuildInfo(version, commit string) {
	m.gaugeVec(Description{
		Name:   "dicer_build_info",
		Labels: []string{"version", "commit", "go_version"},
		Help:   "Build information for the running daemon. Always 1.",
		Doc:    "The build is in its labels.",
		Group:  GroupDaemon,
	}).WithLabelValues(version, commit, runtime.Version()).Set(1)
}

// gaugeVec registers a gauge vector described by d.
func (m *Metrics) gaugeVec(d Description) *prometheus.GaugeVec {
	d.Type = "gauge"
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: d.Name, Help: d.Help}, d.Labels)
	m.register(d, g)

	return g
}

// counter registers a counter described by d, which has no labels.
func (m *Metrics) counter(d Description) prometheus.Counter {
	d.Type = "counter"
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: d.Name, Help: d.Help})
	m.register(d, c)

	return c
}

// counterVec registers a counter vector described by d.
func (m *Metrics) counterVec(d Description) *prometheus.CounterVec {
	d.Type = "counter"
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: d.Name, Help: d.Help}, d.Labels)
	m.register(d, c)

	return c
}

// histogram registers a histogram described by d, which has no labels.
func (m *Metrics) histogram(d Description, buckets []float64) prometheus.Histogram {
	d.Type = "histogram"
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Name: d.Name, Help: d.Help, Buckets: buckets})
	m.register(d, h)

	return h
}

// histogramVec registers a histogram vector described by d.
func (m *Metrics) histogramVec(d Description, buckets []float64) *prometheus.HistogramVec {
	d.Type = "histogram"
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: d.Name, Help: d.Help, Buckets: buckets}, d.Labels)
	m.register(d, h)

	return h
}

// register registers c and adds d to the reference. Every caller is this
// package registering a metric it just built, so a duplicate is a
// programming error and panicking at startup is the right response.
func (m *Metrics) register(d Description, c prometheus.Collector) {
	m.registry.MustRegister(c)
	m.reference = append(m.reference, d)
}

// outcome maps an error to the label value describing it.
func outcome(err error) string {
	if err != nil {
		return outcomeError
	}
	return outcomeSuccess
}
