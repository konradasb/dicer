// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// A source left nil exports no series at all, rather than a misleading zero.
func TestNilSourcesExportNoGauges(t *testing.T) {
	body := scrape(t, New(Options{}))

	for _, unwanted := range []string{"dicer_instances", "dicer_network_addresses", "dicer_images"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("scrape contains %q with no source registered:\n%s", unwanted, body)
		}
	}
}

func TestBuildInfoCarriesTheBuild(t *testing.T) {
	body := scrape(t, New(Options{Version: "v1.2.3", Commit: "abc123"}))

	if !strings.Contains(body, `version="v1.2.3"`) || !strings.Contains(body, `commit="abc123"`) {
		t.Errorf("build info does not carry the build:\n%s", body)
	}
}

// The endpoint serves this daemon's registry alone, so a dependency that
// registers into the default registry cannot appear on it.
func TestHandlerServesOnlyOurRegistry(t *testing.T) {
	m := New(Options{})

	if got := testutil.CollectAndCount(m.instance.operations); got != 0 {
		t.Fatalf("a fresh Metrics already has %d operation series", got)
	}

	body := scrape(t, m)
	if !strings.Contains(body, "dicer_build_info") {
		t.Errorf("scrape is missing this daemon's own metrics:\n%s", body)
	}
	if strings.Contains(body, "promhttp_metric_handler") {
		t.Errorf("scrape carries the default registry's metrics:\n%s", body)
	}
}

// scrape returns what a Prometheus server would read from the endpoint.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want %d", rec.Code, http.StatusOK)
	}

	return rec.Body.String()
}
