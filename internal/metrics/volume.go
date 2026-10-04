// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import "github.com/prometheus/client_golang/prometheus"

// VolumeSummary is a point-in-time summary of the volumes defined on this
// host.
type VolumeSummary struct {
	Count int

	// SizeBytes is the volumes' total size, as their guests see it.
	SizeBytes int64

	// DiskBytes is the disk the volumes take up, which for sparse files is
	// less than their size.
	DiskBytes int64
}

// volumeCollector exports the volumes defined and the disk they take up.
// See collector.go for why this is read per scrape.
type volumeCollector struct {
	source func() VolumeSummary

	count *prometheus.Desc
	size  *prometheus.Desc
	disk  *prometheus.Desc
}

func (m *Metrics) newVolumeCollector(source func() VolumeSummary) *volumeCollector {
	return &volumeCollector{
		source: source,
		count: m.desc(Description{
			Name:  "dicer_volumes",
			Help:  "Volumes defined on this host.",
			Group: GroupVolumes,
		}),
		size: m.desc(Description{
			Name:  "dicer_volume_size_bytes",
			Help:  "Total size of those volumes, as their guests see it.",
			Group: GroupVolumes,
		}),
		disk: m.desc(Description{
			Name: "dicer_volume_disk_bytes",
			Help: "Disk those volumes take up on this host.",
			Doc: "Volumes are sparse files that take up disk as guests write to them, so it is less " +
				"than `dicer_volume_size_bytes` until they fill. A block a guest frees may stay taken.",
			Group: GroupVolumes,
		}),
	}
}

func (c *volumeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.count
	ch <- c.size
	ch <- c.disk
}

func (c *volumeCollector) Collect(ch chan<- prometheus.Metric) {
	summary := c.source()

	ch <- gauge(c.count, float64(summary.Count))
	ch <- gauge(c.size, float64(summary.SizeBytes))
	ch <- gauge(c.disk, float64(summary.DiskBytes))
}
