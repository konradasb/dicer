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

func TestRecordKernelFetch(t *testing.T) {
	m := New(Options{})

	m.RecordKernelFetch(nil, time.Second, 4096)
	m.RecordKernelFetch(errors.New("checksum mismatch"), time.Second, 1024)

	if got := testutil.ToFloat64(m.kernel.fetches.WithLabelValues(outcomeSuccess)); got != 1 {
		t.Errorf("successful fetches = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.kernel.fetches.WithLabelValues(outcomeError)); got != 1 {
		t.Errorf("failed fetches = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.kernel.fetchedBytes); got != 5120 {
		t.Errorf("fetched bytes = %v, want 5120", got)
	}
}

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
