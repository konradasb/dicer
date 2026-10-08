// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"strings"
	"testing"
)

func TestKernelGaugesAreReadPerScrape(t *testing.T) {
	summary := KernelSummary{Count: 2, DiskBytes: 700}

	m := New(Options{Sources: Sources{
		Kernels: func() KernelSummary { return summary },
	}})

	body := scrape(t, m)
	for _, want := range []string{"dicer_kernels 2", "dicer_kernel_disk_bytes 700"} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %q:\n%s", want, body)
		}
	}
}
