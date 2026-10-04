// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/konradasb/dicer/internal/types"
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

// TestReferenceListsEverything checks every served Dicer metric is in the
// reference, as it is served.
func TestReferenceListsEverything(t *testing.T) {
	m := New(Options{Sources: Sources{
		Instances: func() InstanceSummary {
			return InstanceSummary{ByState: map[string]int{"running": 1}, ByHealth: map[string]int{"healthy": 1}}
		},
		Networks: func() []NetworkSummary { return []NetworkSummary{{Name: "default"}} },
		Images:   func() ImageSummary { return ImageSummary{} },
		Kernels:  func() KernelSummary { return KernelSummary{} },
		Volumes:  func() VolumeSummary { return VolumeSummary{} },
		InstanceStats: func() []types.InstanceStats {
			return []types.InstanceStats{{InstanceID: "i-web", Name: "web"}}
		},
	}})

	// Every vector is empty until something is recorded, and an empty one
	// is not served.
	m.RecordInstanceOperation("start", nil, time.Second)
	m.RecordInstanceRestart()
	m.RecordImagePull(errors.New("unreachable"), time.Second, 1)
	m.RecordImageConversion(time.Second)
	m.RecordImageCacheLookup(true)
	m.RecordImageCollected("unused")
	m.RecordImageGCReclaimed(1)
	m.RecordKernelFetch(nil, time.Second, 1)
	m.recordCall("/dicerd.v1.InstanceService/Start", nil, time.Second)

	listed := map[string]Description{}
	for _, d := range m.Reference() {
		listed[d.Name] = d

		if d.Help == "" || d.Group == "" || d.Type == "" {
			t.Errorf("%s: help, group and type are all required: %+v", d.Name, d)
		}
	}

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}

	served := 0
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), "dicer_") {
			continue
		}
		served++

		d, ok := listed[f.GetName()]
		if !ok {
			t.Errorf("%s is served but not in the reference", f.GetName())
			continue
		}
		if want := strings.ToLower(f.GetType().String()); d.Type != want {
			t.Errorf("%s is a %s in the reference, and served as a %s", d.Name, d.Type, want)
		}
		if f.GetHelp() != d.Help {
			t.Errorf("%s's help is %q in the reference, and served as %q", d.Name, d.Help, f.GetHelp())
		}

		var labels []string
		for _, l := range f.GetMetric()[0].GetLabel() {
			labels = append(labels, l.GetName())
		}
		if want := slices.Sorted(slices.Values(d.Labels)); !slices.Equal(labels, want) {
			t.Errorf("%s has labels %v in the reference, and is served with %v", d.Name, want, labels)
		}
	}

	if served != len(listed) {
		t.Errorf("%d metrics served, %d in the reference: this test records into too few", served, len(listed))
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
