// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordDNSQuery(t *testing.T) {
	m := New(Options{})

	m.RecordDNSQuery("default", "local")
	m.RecordDNSQuery("default", "local")
	m.RecordDNSQuery("shop", "failed")
	m.RecordDNSForward("shop", 3*time.Second)

	if got := testutil.ToFloat64(m.dns.queries.WithLabelValues("default", "local")); got != 2 {
		t.Errorf("local queries on default = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.dns.queries.WithLabelValues("shop", "failed")); got != 1 {
		t.Errorf("failed queries on shop = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(m.dns.forwardDuration); got != 1 {
		t.Errorf("forward duration series = %d, want 1", got)
	}
}
