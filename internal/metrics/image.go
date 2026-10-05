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

// ImageSummary is a point-in-time summary of the images held on this host.
type ImageSummary struct {
	Count     int
	DiskBytes int64
}

// imageMetrics measures pulling container images and converting them to the
// disks guests boot from.
type imageMetrics struct {
	pulls              *prometheus.CounterVec
	pullDuration       prometheus.Histogram
	pulledBytes        prometheus.Counter
	conversionDuration prometheus.Histogram
	cacheLookups       *prometheus.CounterVec
	gcCollected        *prometheus.CounterVec
	gcReclaimed        prometheus.Counter
}

func (m *Metrics) newImageMetrics() imageMetrics {
	return imageMetrics{
		pulls: m.counterVec(Description{
			Name:   "dicer_image_pulls_total",
			Labels: []string{"outcome"},
			Help:   "Image pulls that reached a registry, by outcome. Cache hits are not pulls.",
			Doc:    "`outcome` is `success` or `error`.",
			Group:  GroupImages,
		}),

		pullDuration: m.histogram(Description{
			Name:  "dicer_image_pull_duration_seconds",
			Help:  "Time a pull took, from resolving the reference to a bootable disk.",
			Group: GroupImages,
		}, prometheus.ExponentialBuckets(0.5, 2, 12)),

		pulledBytes: m.counter(Description{
			Name:  "dicer_image_pulled_bytes_total",
			Help:  "Compressed layer bytes pulls downloaded from registries.",
			Group: GroupImages,
		}),

		conversionDuration: m.histogram(Description{
			Name:  "dicer_image_conversion_duration_seconds",
			Help:  "Time spent packing an unpacked image into its EROFS disk.",
			Group: GroupImages,
		}, prometheus.ExponentialBuckets(0.25, 2, 12)),

		cacheLookups: m.counterVec(Description{
			Name:   "dicer_image_cache_lookups_total",
			Labels: []string{"result"},
			Help:   "Image lookups by whether this host already held the image.",
			Doc:    "`result` is `hit` or `miss`.",
			Group:  GroupImages,
		}),

		gcCollected: m.counterVec(Description{
			Name:   "dicer_image_gc_collected_total",
			Labels: []string{"reason"},
			Help:   "Images garbage collection removed, by reason (unused, size).",
			Group:  GroupImages,
		}),

		gcReclaimed: m.counter(Description{
			Name:  "dicer_image_gc_reclaimed_bytes_total",
			Help:  "Disk image garbage collection gave back: bootable disks and cached layers.",
			Group: GroupImages,
		}),
	}
}

// RecordImagePull records a registry pull: its outcome, duration and
// downloaded bytes.
func (m *Metrics) RecordImagePull(err error, d time.Duration, downloadedBytes int64) {
	m.image.pulls.WithLabelValues(outcome(err)).Inc()
	m.image.pullDuration.Observe(d.Seconds())
	m.image.pulledBytes.Add(float64(downloadedBytes))
}

// RecordImageConversion records the time taken to pack an unpacked image into
// the disk a guest boots from.
func (m *Metrics) RecordImageConversion(d time.Duration) {
	m.image.conversionDuration.Observe(d.Seconds())
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
	source func() ImageSummary

	count *prometheus.Desc
	disk  *prometheus.Desc
}

func (m *Metrics) newImageCollector(source func() ImageSummary) *imageCollector {
	return &imageCollector{
		source: source,
		count: m.descriptor(Description{
			Name:  "dicer_images",
			Help:  "Images held on this host.",
			Group: GroupImages,
		}),
		disk: m.descriptor(Description{
			Name:  "dicer_image_disk_bytes",
			Help:  "Total size of the bootable disks those images were converted to.",
			Group: GroupImages,
		}),
	}
}

func (c *imageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.count
	ch <- c.disk
}

func (c *imageCollector) Collect(ch chan<- prometheus.Metric) {
	summary := c.source()

	ch <- gaugeReading(c.count, float64(summary.Count))
	ch <- gaugeReading(c.disk, float64(summary.DiskBytes))
}

// RecordImageGCCollected records an image garbage collection removed, and
// why.
func (m *Metrics) RecordImageGCCollected(reason string) {
	m.image.gcCollected.WithLabelValues(reason).Inc()
}

// RecordImageGCReclaimed records the bytes garbage collection gave back.
func (m *Metrics) RecordImageGCReclaimed(bytes int64) {
	m.image.gcReclaimed.Add(float64(bytes))
}
