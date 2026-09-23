// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Cache lookup label values.
const (
	resultHit  = "hit"
	resultMiss = "miss"
)

// ImageStats is a point-in-time summary of the images held on this host.
type ImageStats struct {
	Count     int
	DiskBytes int64
}

// imageMetrics measures pulling container images and converting them to the
// disks guests boot from.
type imageMetrics struct {
	pulls           *prometheus.CounterVec
	pullDuration    prometheus.Histogram
	downloadedBytes prometheus.Counter
	convertDuration prometheus.Histogram
	cacheLookups    *prometheus.CounterVec
	collected       *prometheus.CounterVec
	gcReclaimed     prometheus.Counter
}

func newImageMetrics() imageMetrics {
	return imageMetrics{
		pulls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "image_pulls_total",
			Help:      "Image pulls that reached a registry, by outcome. Cache hits are not pulls.",
		}, []string{"outcome"}),

		pullDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "image_pull_duration_seconds",
			Help:      "Time a pull took, from resolving the reference to a bootable disk.",
			Buckets:   prometheus.ExponentialBuckets(0.5, 2, 12),
		}),

		downloadedBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "image_downloaded_bytes_total",
			Help:      "Compressed layer bytes downloaded from registries.",
		}),

		convertDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "image_conversion_duration_seconds",
			Help:      "Time spent packing an unpacked image into its EROFS disk.",
			Buckets:   prometheus.ExponentialBuckets(0.25, 2, 12),
		}),

		cacheLookups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "image_cache_lookups_total",
			Help:      "Image lookups by whether this host already held the image.",
		}, []string{"result"}),

		collected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "image_gc_collected_total",
			Help:      "Images garbage collection removed, by reason (unused, size).",
		}, []string{"reason"}),

		gcReclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "image_gc_reclaimed_bytes_total",
			Help:      "Disk image garbage collection gave back: bootable disks and cached layers.",
		}),
	}
}

func (im imageMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		im.pulls, im.pullDuration, im.downloadedBytes, im.convertDuration, im.cacheLookups,
		im.collected, im.gcReclaimed,
	}
}

// RecordImagePull records a registry pull: its outcome, duration and
// downloaded bytes.
func (m *Metrics) RecordImagePull(err error, d time.Duration, downloadedBytes int64) {
	m.image.pulls.WithLabelValues(outcome(err)).Inc()
	m.image.pullDuration.Observe(d.Seconds())
	m.image.downloadedBytes.Add(float64(downloadedBytes))
}

// RecordImageConversion records the time taken to pack an unpacked image into
// the disk a guest boots from.
func (m *Metrics) RecordImageConversion(d time.Duration) {
	m.image.convertDuration.Observe(d.Seconds())
}

// RecordImageCacheLookup records whether an image was already held locally.
func (m *Metrics) RecordImageCacheLookup(hit bool) {
	result := resultMiss
	if hit {
		result = resultHit
	}

	m.image.cacheLookups.WithLabelValues(result).Inc()
}

// imageCollector exports what the local image store currently holds. See
// collector.go for why this is read per scrape.
type imageCollector struct {
	source func() ImageStats

	count *prometheus.Desc
	bytes *prometheus.Desc
}

func newImageCollector(source func() ImageStats) *imageCollector {
	return &imageCollector{
		source: source,
		count:  desc("images", "Images held on this host."),
		bytes: desc("image_disk_bytes",
			"Total size of the bootable disks those images were converted to."),
	}
}

func (c *imageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.count
	ch <- c.bytes
}

func (c *imageCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.source()

	ch <- gauge(c.count, float64(stats.Count))
	ch <- gauge(c.bytes, float64(stats.DiskBytes))
}

// RecordImageCollected records an image garbage collection removed, and why.
func (m *Metrics) RecordImageCollected(reason string) {
	m.image.collected.WithLabelValues(reason).Inc()
}

// RecordImageGCReclaimed records the bytes garbage collection gave back.
func (m *Metrics) RecordImageGCReclaimed(bytes int64) {
	m.image.gcReclaimed.Add(float64(bytes))
}
