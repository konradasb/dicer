// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordImagePull(t *testing.T) {
	m := New(Options{})

	m.RecordImagePull(nil, time.Second, 4096)
	m.RecordImagePull(errors.New("registry unreachable"), time.Second, 0)

	if got := testutil.ToFloat64(m.image.pulls.WithLabelValues(outcomeSuccess)); got != 1 {
		t.Errorf("successful pulls = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.image.pulls.WithLabelValues(outcomeError)); got != 1 {
		t.Errorf("failed pulls = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.image.downloadedBytes); got != 4096 {
		t.Errorf("downloaded bytes = %v, want 4096", got)
	}
}

func TestRecordImageCacheLookup(t *testing.T) {
	m := New(Options{})

	m.RecordImageCacheLookup(true)
	m.RecordImageCacheLookup(false)
	m.RecordImageCacheLookup(false)

	if got := testutil.ToFloat64(m.image.cacheLookups.WithLabelValues(resultHit)); got != 1 {
		t.Errorf("hits = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.image.cacheLookups.WithLabelValues(resultMiss)); got != 2 {
		t.Errorf("misses = %v, want 2", got)
	}
}

func TestImageGaugesAreReadPerScrape(t *testing.T) {
	stats := ImageStats{Count: 3, DiskBytes: 900}

	m := New(Options{Sources: Sources{
		Images: func() ImageStats { return stats },
	}})

	body := scrape(t, m)
	for _, want := range []string{"dicer_images 3", "dicer_image_disk_bytes 900"} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %q:\n%s", want, body)
		}
	}
}
