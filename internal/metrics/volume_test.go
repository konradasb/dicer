// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"strings"
	"testing"
)

func TestVolumeGaugesAreReadPerScrape(t *testing.T) {
	summary := VolumeSummary{Count: 2, SizeBytes: 2048, DiskBytes: 512}

	m := New(Options{Sources: Sources{
		Volumes: func() VolumeSummary { return summary },
	}})

	body := scrape(t, m)
	for _, want := range []string{
		"dicer_volumes 2",
		"dicer_volume_size_bytes 2048",
		"dicer_volume_disk_bytes 512",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %q:\n%s", want, body)
		}
	}
}
