// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

// TestLoadConfigMissingFileUsesDefaults covers first run: the defaults are a
// working single-host configuration, so a missing file must not be an error.
func TestLoadConfigMissingFileUsesDefaults(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	want := defaultConfig()
	if cfg.DataDir != want.DataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, want.DataDir)
	}
	if cfg.API.Socket.Path != want.API.Socket.Path {
		t.Errorf("API.Socket.Path = %q, want %q", cfg.API.Socket.Path, want.API.Socket.Path)
	}
	// The API is served on the network only when asked to be.
	if cfg.API.TCP.Enabled() {
		t.Error("the API listens on TCP by default")
	}
	if cfg.LogLevel != want.LogLevel {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, want.LogLevel)
	}
}

func TestLoadConfigOverridesDefaults(t *testing.T) {
	path := writeConfig(t, `
data_dir: /srv/dicer
api:
  socket:
    path: /tmp/dicer.sock
  tcp:
    listen: 0.0.0.0:7443
log_level: debug
network:
  uplink_interface: eth1
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.DataDir != "/srv/dicer" {
		t.Errorf("DataDir = %q, want /srv/dicer", cfg.DataDir)
	}
	if cfg.API.Socket.Path != "/tmp/dicer.sock" {
		t.Errorf("API.Socket.Path = %q, want /tmp/dicer.sock", cfg.API.Socket.Path)
	}
	if cfg.API.TCP.Listen != "0.0.0.0:7443" {
		t.Errorf("API.TCP.Listen = %q, want 0.0.0.0:7443", cfg.API.TCP.Listen)
	}
	if cfg.Network.UplinkInterface != "eth1" {
		t.Errorf("UplinkInterface = %q, want eth1", cfg.Network.UplinkInterface)
	}
}

// TestLoadConfigPartialKeepsDefaults: a file that sets one key must not blank
// out everything it does not mention.
func TestLoadConfigPartialKeepsDefaults(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, "log_level: warn\n"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want warn", cfg.LogLevel)
	}
	if cfg.DataDir != defaultConfig().DataDir {
		t.Errorf("DataDir = %q, want the default to survive", cfg.DataDir)
	}
	if cfg.API.Socket.Mode != defaultConfig().API.Socket.Mode {
		t.Errorf("API.Socket.Mode = %o, want the default to survive", cfg.API.Socket.Mode)
	}
}

// A key the daemon does not know is refused rather than ignored, so that a
// typo cannot silently leave a setting
// at its default.
func TestLoadConfigRejectsUnknownKeys(t *testing.T) {
	if _, err := loadConfig(writeConfig(t, "listen: /tmp/dicer.sock\n")); err == nil {
		t.Error("expected an error for an unknown key")
	}
}

func TestLoadConfigAcceptsEmptyFile(t *testing.T) {
	if _, err := loadConfig(writeConfig(t, "")); err != nil {
		t.Errorf("loadConfig of an empty file: %v", err)
	}
}

func TestLoadConfigRejectsMalformedYAML(t *testing.T) {
	if _, err := loadConfig(writeConfig(t, "{{{not yaml")); err == nil {
		t.Error("expected an error for malformed YAML")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "defaults are valid", mutate: func(*Config) {}},
		{name: "debug level", mutate: func(c *Config) { c.LogLevel = "debug" }},
		{name: "error level", mutate: func(c *Config) { c.LogLevel = "error" }},
		{name: "unknown log level", mutate: func(c *Config) { c.LogLevel = "verbose" }, wantErr: true},
		{name: "empty log level", mutate: func(c *Config) { c.LogLevel = "" }, wantErr: true},
		{name: "missing data dir", mutate: func(c *Config) { c.DataDir = "" }, wantErr: true},
		{name: "missing socket", mutate: func(c *Config) { c.API.Socket.Path = "" }, wantErr: true},
		{name: "tcp listener", mutate: func(c *Config) { c.API.TCP.Listen = "0.0.0.0:7443" }},
		{name: "tcp without port", mutate: func(c *Config) { c.API.TCP.Listen = "0.0.0.0" }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected an error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	if _, err := loadConfig(writeConfig(t, "log_level: verbose\n")); err == nil {
		t.Error("expected loadConfig to validate what it parsed")
	}
}

func TestMetricsAreOffByDefault(t *testing.T) {
	cfg := defaultConfig()

	if cfg.Metrics.Enable {
		t.Error("metrics are enabled by default; the daemon should open no network listener unless asked")
	}
	if cfg.Metrics.Listen != defaultMetricsListen {
		t.Errorf("Metrics.Listen = %q, want %q", cfg.Metrics.Listen, defaultMetricsListen)
	}
}

func TestLoadConfigEnablesMetrics(t *testing.T) {
	path := writeConfig(t, `
metrics:
  enable: true
  listen: 0.0.0.0:9101
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if !cfg.Metrics.Enable {
		t.Error("Metrics.Enable = false, want true")
	}
	if cfg.Metrics.Listen != "0.0.0.0:9101" {
		t.Errorf("Metrics.Listen = %q, want 0.0.0.0:9101", cfg.Metrics.Listen)
	}
}

func TestValidateRejectsBadMetricsConfig(t *testing.T) {
	tests := []struct {
		name    string
		metrics MetricsConfig
		wantErr bool
	}{
		{
			name:    "disabled is never invalid",
			metrics: MetricsConfig{},
		},
		{
			name:    "valid",
			metrics: MetricsConfig{Enable: true, Listen: "127.0.0.1:9101"},
		},
		{
			name:    "no listen address",
			metrics: MetricsConfig{Enable: true},
			wantErr: true,
		},
		{
			name:    "address without a port",
			metrics: MetricsConfig{Enable: true, Listen: "127.0.0.1"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Metrics = tt.metrics

			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResourcesDefaults(t *testing.T) {
	cfg := defaultConfig()

	capacity, err := cfg.Resources.capacity(4, 32<<30)
	if err != nil {
		t.Fatalf("capacity: %v", err)
	}

	// vCPUs shared four to a CPU; memory not overcommitted, less 1GiB.
	want := types.Resources{VCPUs: 16, MemoryBytes: 31 << 30}
	if got := capacity.Allocatable(); got != want {
		t.Errorf("allocatable = %+v, want %+v", got, want)
	}
}

func TestResourcesReserveMustLeaveSomething(t *testing.T) {
	cfg := defaultConfig()

	if _, err := cfg.Resources.capacity(4, 1<<30); err == nil {
		t.Error("a reserve of all the host's memory was accepted")
	}
}

func TestValidateResources(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ResourcesConfig)
	}{
		{"zero cpu overcommit", func(r *ResourcesConfig) { r.CPUOvercommit = 0 }},
		{"negative memory overcommit", func(r *ResourcesConfig) { r.MemoryOvercommit = -1 }},
		{"negative reserve", func(r *ResourcesConfig) { r.ReservedMemoryBytes = -1 }},
	} {
		cfg := defaultConfig()
		tc.mutate(&cfg.Resources)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestLoadConfigResources(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `
resources:
  cpu_overcommit: 2
  memory_overcommit: 1.5
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.Resources.CPUOvercommit != 2 || cfg.Resources.MemoryOvercommit != 1.5 {
		t.Errorf("resources = %+v, want the file's ratios", cfg.Resources)
	}
	// What the file does not mention keeps its default.
	if cfg.Resources.ReservedMemoryBytes != defaultReservedMemoryBytes {
		t.Errorf("reserve = %d, want the default", cfg.Resources.ReservedMemoryBytes)
	}
}

func TestLoadConfigImageGC(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `
images:
  gc_max_unused_age: 168h
  gc_max_size: 50GiB
  gc_interval: 30m
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	got := cfg.Images.gcPolicy()
	if got.MaxUnusedAge != 168*time.Hour || got.MaxSize != 50<<30 || !got.Enabled() {
		t.Errorf("policy = %+v, want a week and 50GiB", got)
	}
	if cfg.Images.GCInterval != 30*time.Minute {
		t.Errorf("interval = %v, want 30m", cfg.Images.GCInterval)
	}

	// A size in plain bytes is taken too.
	cfg, err = loadConfig(writeConfig(t, "images:\n  gc_max_size: 1073741824\n"))
	if err != nil || cfg.Images.gcPolicy().MaxSize != 1<<30 {
		t.Errorf("gc_max_size in bytes = %+v, %v", cfg.Images, err)
	}
}

// Garbage collection is off unless a limit is set, and runs hourly when one
// is.
func TestImageGCIsOffByDefault(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, "log_level: info\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Images.gcPolicy().Enabled() {
		t.Errorf("policy = %+v, want nothing collected", cfg.Images.gcPolicy())
	}
	if cfg.Images.GCInterval != time.Hour {
		t.Errorf("interval = %v, want an hour", cfg.Images.GCInterval)
	}
}

func TestLoadConfigRejectsBadImageGC(t *testing.T) {
	for _, body := range []string{
		"images:\n  gc_max_size: lots\n",
		"images:\n  gc_max_unused_age: a week\n",
		"images:\n  gc_max_unused_age: -1h\n",
		"images:\n  gc_interval: 0s\n",
		"images:\n  gc_max_unused_age: 7d\n",
	} {
		if _, err := loadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("loadConfig accepted %q", body)
		}
	}
}

func TestLoadConfigEvents(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, "log_level: info\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Events.MaxCount != 10000 || cfg.Events.MaxAge != 0 {
		t.Errorf("default events = %+v, want 10000 kept, of any age", cfg.Events)
	}

	cfg, err = loadConfig(writeConfig(t, "events:\n  max_count: 500\n  max_age: 720h\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Events.MaxCount != 500 || cfg.Events.MaxAge != 720*time.Hour {
		t.Errorf("events = %+v, want 500 kept, for 30 days", cfg.Events)
	}

	for _, body := range []string{"events:\n  max_count: 0\n", "events:\n  max_age: -1h\n"} {
		if _, err := loadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("loadConfig accepted %q", body)
		}
	}
}
