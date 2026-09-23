// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/docker/go-units"
	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer/internal/defaults"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/hostnet"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/types"
)

const (
	defaultSocketMode = 0o660

	// defaultMetricsListen is loopback on port 9101; 9100 is node_exporter's.
	defaultMetricsListen = "127.0.0.1:9101"

	defaultCPUOvercommit       = 4
	defaultMemoryOvercommit    = 1
	defaultReservedMemoryBytes = 1 << 30

	defaultGCInterval = time.Hour
)

// Config is the daemon configuration. See example.yml for every key.
type Config struct {
	// DataDir holds definitions, images and disks. Persistent.
	DataDir string `yaml:"data_dir,omitempty"`

	// RunDir holds runtime state. Expected to be a tmpfs.
	RunDir string `yaml:"run_dir,omitempty"`

	API       APIConfig       `yaml:"api"`
	Resources ResourcesConfig `yaml:"resources"`
	Network   NetworkConfig   `yaml:"network"`
	Defaults  DefaultsConfig  `yaml:"defaults"`
	Metrics   MetricsConfig   `yaml:"metrics"`
	Images    ImagesConfig    `yaml:"images"`
	Events    EventsConfig    `yaml:"events"`

	// LogLevel is debug, info, warn or error.
	LogLevel string `yaml:"log_level,omitempty"`
}

// APIConfig controls where the API is served: always on a Unix socket, and
// on TCP if Listen is set.
type APIConfig struct {
	Socket SocketConfig `yaml:"socket"`
	TCP    TCPConfig    `yaml:"tcp"`
}

// SocketConfig controls the API socket. Access is controlled by its file
// permissions.
type SocketConfig struct {
	Path string `yaml:"path,omitempty"`
	Mode uint32 `yaml:"mode,omitempty"`
}

// TCPConfig controls the network listener. Without TLS it is
// unauthenticated and unencrypted.
type TCPConfig struct {
	// Listen is the host:port to serve on. Empty disables the listener.
	Listen string `yaml:"listen,omitempty"`

	TLS TLSConfig `yaml:"tls"`
}

// TLSConfig configures TLS on the network listener. The certificate and key
// are reloaded when they change on disk; the client CA is read at startup.
type TLSConfig struct {
	// CertFile and KeyFile are the daemon's PEM certificate and key.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`

	// ClientCAFile holds the PEM authorities client certificates must be
	// issued by. Empty accepts every client.
	ClientCAFile string `yaml:"client_ca_file,omitempty"`
}

// Enabled reports whether the listener is served over TLS.
func (t TLSConfig) Enabled() bool {
	return t.CertFile != "" || t.KeyFile != ""
}

// RequiresClientCert reports whether callers must present a certificate.
func (t TLSConfig) RequiresClientCert() bool {
	return t.ClientCAFile != ""
}

// validate reports whether the TLS settings are a usable combination. The
// files are read when the listener is built.
func (t TLSConfig) validate() error {
	switch {
	case t.CertFile != "" && t.KeyFile == "":
		return errors.New("api.tcp.tls.cert_file is set without api.tcp.tls.key_file")
	case t.KeyFile != "" && t.CertFile == "":
		return errors.New("api.tcp.tls.key_file is set without api.tcp.tls.cert_file")
	case t.ClientCAFile != "" && !t.Enabled():
		return errors.New(
			"api.tcp.tls.client_ca_file needs a certificate of its own: " +
				"set api.tcp.tls.cert_file and api.tcp.tls.key_file too")
	}

	return nil
}

// Enabled reports whether the API is served over TCP.
func (t TCPConfig) Enabled() bool {
	return t.Listen != ""
}

// ResourcesConfig sets how much of the host instances may be given: host
// CPUs × CPUOvercommit, and (host memory − ReservedMemoryBytes) ×
// MemoryOvercommit. A start that would exceed either is refused.
type ResourcesConfig struct {
	CPUOvercommit       float64 `yaml:"cpu_overcommit,omitempty"`
	MemoryOvercommit    float64 `yaml:"memory_overcommit,omitempty"`
	ReservedMemoryBytes int64   `yaml:"reserved_memory_bytes,omitempty"`
}

// validate reports whether the resource settings are usable.
func (r *ResourcesConfig) validate() error {
	if r.CPUOvercommit <= 0 {
		return fmt.Errorf("resources.cpu_overcommit must be greater than 0, not %g", r.CPUOvercommit)
	}
	if r.MemoryOvercommit <= 0 {
		return fmt.Errorf("resources.memory_overcommit must be greater than 0, not %g", r.MemoryOvercommit)
	}
	if r.ReservedMemoryBytes < 0 {
		return fmt.Errorf("resources.reserved_memory_bytes must not be negative, not %d", r.ReservedMemoryBytes)
	}

	return nil
}

// capacity is what instances may be given on a host with the given CPUs and
// memory.
func (r *ResourcesConfig) capacity(cpus int, memoryBytes int64) (types.Capacity, error) {
	if r.ReservedMemoryBytes >= memoryBytes {
		return types.Capacity{}, fmt.Errorf(
			"resources.reserved_memory_bytes (%d) leaves nothing of the host's %d bytes of memory for instances",
			r.ReservedMemoryBytes, memoryBytes)
	}

	return types.Capacity{
		Host:                types.Resources{VCPUs: cpus, MemoryBytes: memoryBytes},
		ReservedMemoryBytes: r.ReservedMemoryBytes,
		CPUOvercommit:       r.CPUOvercommit,
		MemoryOvercommit:    r.MemoryOvercommit,
	}, nil
}

// NetworkConfig controls host networking.
type NetworkConfig struct {
	// UplinkInterface is the interface NAT traffic leaves by. Empty detects
	// it from the default route.
	UplinkInterface string `yaml:"uplink_interface,omitempty"`

	// UplinkCapacityBps caps per-instance bandwidth shaping.
	UplinkCapacityBps int64 `yaml:"uplink_capacity_bps,omitempty"`

	// UploadBurstMultiplier and DownloadBurstMultiplier scale an instance's
	// rate limit into its burst allowance.
	UploadBurstMultiplier   int `yaml:"upload_burst_multiplier,omitempty"`
	DownloadBurstMultiplier int `yaml:"download_burst_multiplier,omitempty"`
}

// DefaultsConfig names the kernel and network an instance gets when it names
// none. Unset, an instance gets the only one there is.
type DefaultsConfig struct {
	Kernel  string `yaml:"kernel,omitempty"`
	Network string `yaml:"network,omitempty"`
}

// validate reports whether the defaults are valid names.
func (d *DefaultsConfig) validate() error {
	if d.Kernel != "" {
		if err := naming.Validate(d.Kernel); err != nil {
			return fmt.Errorf("defaults.kernel: %w", err)
		}
	}
	if d.Network != "" {
		if err := naming.Validate(d.Network); err != nil {
			return fmt.Errorf("defaults.network: %w", err)
		}
	}
	return nil
}

// MetricsConfig controls the unauthenticated Prometheus endpoint. Metrics
// are always recorded; this only decides whether they are served.
type MetricsConfig struct {
	Enable bool   `yaml:"enable,omitempty"`
	Listen string `yaml:"listen,omitempty"`
}

// ImagesConfig configures image garbage collection, which removes unused
// images older than GCMaxUnusedAge, and the least recently used while the
// store exceeds GCMaxSize. Zero limits disable it.
type ImagesConfig struct {
	GCMaxUnusedAge time.Duration `yaml:"gc_max_unused_age,omitempty"`
	GCMaxSize      byteSize      `yaml:"gc_max_size,omitempty"`
	GCInterval     time.Duration `yaml:"gc_interval,omitempty"`
}

func (i *ImagesConfig) validate() error {
	switch {
	case i.GCMaxUnusedAge < 0:
		return errors.New("images.gc_max_unused_age cannot be negative")
	case i.GCMaxSize < 0:
		return errors.New("images.gc_max_size cannot be negative")
	case i.GCInterval <= 0:
		return errors.New("images.gc_interval must be positive")
	}
	return nil
}

// gcPolicy is the garbage collection policy the configuration asks for.
func (i *ImagesConfig) gcPolicy() image.GCPolicy {
	return image.GCPolicy{MaxUnusedAge: i.GCMaxUnusedAge, MaxSize: int64(i.GCMaxSize)}
}

// byteSize is a size in bytes, written in YAML as a number or with a unit
// such as 50GiB.
type byteSize int64

func (b *byteSize) UnmarshalYAML(value *yaml.Node) error {
	var n int64
	if value.Decode(&n) == nil {
		*b = byteSize(n)
		return nil
	}

	n, err := units.RAMInBytes(value.Value)
	if err != nil {
		return fmt.Errorf("invalid size %q: want a size like 512MiB or 50GiB", value.Value)
	}
	*b = byteSize(n)
	return nil
}

// EventsConfig bounds the events log. A zero MaxAge means no age limit.
type EventsConfig struct {
	MaxCount int           `yaml:"max_count,omitempty"`
	MaxAge   time.Duration `yaml:"max_age,omitempty"`
}

func (e *EventsConfig) validate() error {
	switch {
	case e.MaxCount <= 0:
		return errors.New("events.max_count must be positive")
	case e.MaxAge < 0:
		return errors.New("events.max_age cannot be negative")
	}
	return nil
}

// Validate reports whether the configuration is usable.
func (c *Config) Validate() error {
	if _, err := c.logLevel(); err != nil {
		return err
	}

	if c.DataDir == "" {
		return errors.New("data_dir is required")
	}
	if err := c.API.validate(); err != nil {
		return err
	}
	if err := c.Resources.validate(); err != nil {
		return err
	}
	if err := c.Defaults.validate(); err != nil {
		return err
	}
	if err := c.Images.validate(); err != nil {
		return err
	}
	if err := c.Events.validate(); err != nil {
		return err
	}

	return c.Metrics.validate()
}

// validate reports whether the API configuration is usable.
func (a *APIConfig) validate() error {
	if a.Socket.Path == "" {
		return errors.New("api.socket.path is required")
	}

	if !a.TCP.Enabled() {
		return nil
	}

	if _, _, err := net.SplitHostPort(a.TCP.Listen); err != nil {
		return fmt.Errorf("invalid api.tcp.listen %q: want host:port: %w", a.TCP.Listen, err)
	}

	return a.TCP.TLS.validate()
}

// validate reports whether the metrics configuration is usable. A disabled
// endpoint is not checked.
func (m *MetricsConfig) validate() error {
	if !m.Enable {
		return nil
	}

	if m.Listen == "" {
		return errors.New("metrics.listen is required when metrics are enabled")
	}
	if _, _, err := net.SplitHostPort(m.Listen); err != nil {
		return fmt.Errorf("invalid metrics.listen %q: want host:port: %w", m.Listen, err)
	}

	return nil
}

// logLevel parses LogLevel.
func (c *Config) logLevel() (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return 0, fmt.Errorf("invalid log_level %q: want debug, info, warn or error", c.LogLevel)
	}
	return level, nil
}

// defaultConfig returns the configuration a config file overrides.
func defaultConfig() Config {
	return Config{
		DataDir: defaults.DataDir,
		RunDir:  defaults.RunDir,
		API: APIConfig{
			Socket: SocketConfig{Path: defaults.Socket, Mode: defaultSocketMode},
		},
		Resources: ResourcesConfig{
			CPUOvercommit:       defaultCPUOvercommit,
			MemoryOvercommit:    defaultMemoryOvercommit,
			ReservedMemoryBytes: defaultReservedMemoryBytes,
		},
		Network: NetworkConfig{
			UploadBurstMultiplier:   hostnet.DefaultBurstMultiplier,
			DownloadBurstMultiplier: hostnet.DefaultBurstMultiplier,
		},
		LogLevel: "info",
		Metrics:  MetricsConfig{Listen: defaultMetricsListen},
		Images:   ImagesConfig{GCInterval: defaultGCInterval},
		Events:   EventsConfig{MaxCount: events.DefaultMaxCount},
	}
}

// loadConfig reads the configuration file over the defaults. A missing file
// is not an error.
func loadConfig(path string) (*Config, error) {
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return &cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Reject unknown keys so a typo is not silently ignored.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return &cfg, nil
}
