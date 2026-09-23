// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
)

// newTestDaemon returns a daemon with metrics built but no services, which is
// what the endpoint has to cope with at startup.
func newTestDaemon(t *testing.T, cfg MetricsConfig) *daemon {
	t.Helper()

	base := defaultConfig()
	base.Metrics = cfg

	d := &daemon{cfg: &base, logger: slog.New(slog.DiscardHandler)}
	d.metrics = d.newMetrics()

	return d
}

func TestServeMetricsServesTheConfiguredPath(t *testing.T) {
	d := newTestDaemon(t, MetricsConfig{
		Enabled: true,
		Listen:  "127.0.0.1:0",
		Path:    "/custom-metrics",
	})

	listener := reserve(t)
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	d.cfg.Metrics.Listen = addr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopped := make(chan error, 1)
	go func() { stopped <- d.serveMetrics(ctx) }()

	body := get(t, "http://"+addr+"/custom-metrics")
	if !strings.Contains(body, "dicer_build_info") {
		t.Errorf("scrape is missing this daemon's metrics:\n%s", body)
	}

	// The sources are registered before the managers exist. A scrape then
	// must report nothing, not panic.
	if strings.Contains(body, "dicer_instances{") {
		t.Errorf("instance gauges reported before the manager exists:\n%s", body)
	}

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("serveMetrics returned %v after cancellation, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("serveMetrics did not return after its context was cancelled")
	}
}

func TestServeMetricsDisabledServesNothing(t *testing.T) {
	d := newTestDaemon(t, MetricsConfig{Listen: "127.0.0.1:0", Path: "/metrics"})

	if err := d.serveMetrics(context.Background()); err != nil {
		t.Errorf("serveMetrics with the endpoint disabled = %v, want nil", err)
	}
}

// A metrics endpoint that cannot bind must say so rather than run on in
// silence: the daemon treats it as fatal, because a scrape target that is
// quietly absent is how a monitoring gap starts.
func TestServeMetricsReportsAnAddressItCannotBind(t *testing.T) {
	// Hold the port for the duration, so the bind fails because something
	// else has it -- the way an operator meets this -- rather than because
	// of a privilege the test may or may not have. Running as root, which
	// is how this runs in CI, makes a privileged port bind just fine.
	listener := reserve(t)

	d := newTestDaemon(t, MetricsConfig{
		Enabled: true,
		Listen:  listener.Addr().String(),
		Path:    "/metrics",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := d.serveMetrics(ctx)
	if err == nil {
		t.Fatal("serveMetrics on a taken port returned nil, want an error")
	}
	if ctx.Err() != nil {
		t.Error("serveMetrics blocked instead of reporting the bind failure")
	}
}

// reserve opens a listener on a kernel-chosen loopback port and closes it
// when the test ends. Port 0 means the test cannot collide with whatever
// else is listening on the machine.
func reserve(t *testing.T) net.Listener {
	t.Helper()

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	return listener
}

// get reads a URL, retrying until the server is accepting connections.
func get(t *testing.T, url string) string {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url) //nolint:noctx // the deadline below bounds it
		if err == nil {
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d, want %d", url, resp.StatusCode, http.StatusOK)
			}

			return string(body)
		}

		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", url, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNetworkStatsJoinsDefinitionsAndAllocations(t *testing.T) {
	d := newTestDaemon(t, MetricsConfig{})
	openTestStores(t, d)

	// A /24 has 256 addresses, of which the network, broadcast and gateway
	// addresses are not assignable: 253 can be handed out.
	const subnet = "172.20.0.0/24"
	nw := dicer.Network{
		ID: "n-1", Name: "default", Subnet: subnet, Gateway: "172.20.0.1", Bridge: "dicer0",
	}
	if err := d.definitions.CreateNetwork(nw); err != nil {
		t.Fatalf("create network: %v", err)
	}

	for _, id := range []string{"i-1", "i-2"} {
		if _, err := d.addresses.Allocate(nw, id, ""); err != nil {
			t.Fatalf("allocate for %s: %v", id, err)
		}
	}

	stats := d.networkStats()
	if len(stats) != 1 {
		t.Fatalf("got %d networks, want 1: %+v", len(stats), stats)
	}

	got := stats[0]
	if got.Name != nw.Name {
		t.Errorf("Name = %q, want %q", got.Name, nw.Name)
	}
	if got.Allocated != 2 {
		t.Errorf("Allocated = %d, want 2", got.Allocated)
	}
	if want := int64(253 - 2); got.Available != want {
		t.Errorf("Available = %d, want %d", got.Available, want)
	}
}

// A host with no networks reports none, rather than an error or a series.
func TestNetworkStatsWithNoNetworks(t *testing.T) {
	d := newTestDaemon(t, MetricsConfig{})
	openTestStores(t, d)

	if stats := d.networkStats(); len(stats) != 0 {
		t.Errorf("networkStats() = %+v, want none", stats)
	}
}

// The sources are registered before the stores are opened, so they have to
// cope with being called first.
func TestNetworkStatsBeforeTheStoresExist(t *testing.T) {
	d := newTestDaemon(t, MetricsConfig{})

	if stats := d.networkStats(); stats != nil {
		t.Errorf("networkStats() = %+v, want nil before the stores are opened", stats)
	}
}

// openTestStores gives a daemon a definition store and address manager rooted in a
// temporary directory, as openDefinitions does at startup.
func openTestStores(t *testing.T, d *daemon) {
	t.Helper()

	d.cfg.DataDir = t.TempDir()
	if err := d.openDefinitions(); err != nil {
		t.Fatalf("open stores: %v", err)
	}
}
