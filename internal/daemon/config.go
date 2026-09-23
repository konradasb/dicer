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
	"strings"
	"time"

	"github.com/docker/go-units"
	"gopkg.in/yaml.v3"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/events"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/image"
	"github.com/dicer-sh/dicer/internal/vm"
)

// Defaults. The CLI's built-in local remote matches defaultSocketPath.
const (
	defaultSocketPath = "/run/dicer/dicer.sock"
	defaultSocketMode = 0o660
	defaultConfigPath = "/etc/dicerd/config.yaml"

	// defaultMetricsListen binds the metrics endpoint to loopback. It is
	// port 9101 because 9100 is node_exporter's.
	defaultMetricsListen = "127.0.0.1:9101"
	defaultMetricsPath   = "/metrics"

	// The admission defaults: vCPUs shared four to a CPU, memory not
	// overcommitted, and 1GiB kept back for the host. See ResourcesConfig.
	defaultCPUOvercommit       = 4
	defaultMemoryOvercommit    = 1
	defaultReservedMemoryBytes = 1 << 30

	// defaultGCInterval is how often image garbage collection runs, when a
	// limit is set.
	defaultGCInterval = time.Hour
)

// Config is the daemon configuration.
//
// Example:
//
//	data_dir: /var/lib/dicer
//	run_dir: /run/dicer
//	api:
//	  socket:
//	    path: /run/dicer/dicer.sock
//	    mode: 0660
//	  tcp:
//	    listen: 0.0.0.0:7443
//	log_level: info
//	resources:
//	  cpu_overcommit: 4
//	  memory_overcommit: 1
//	  reserved_memory_bytes: 1073741824
//	network:
//	  uplink_interface: eth0
//	defaults:
//	  kernel: vmlinux-6.12
//	  network: default
//	metrics:
//	  enabled: true
//	  listen: 127.0.0.1:9101
//	images:
//	  gc_max_unused_age: 168h
//	  gc_max_size: 50GiB
//	  gc_interval: 1h
//	events:
//	  max_count: 10000
//	  max_age: 720h
type Config struct {
	// DataDir holds instance, network, volume and kernel definitions, plus
	// images and disks. Persistent.
	DataDir string `yaml:"data_dir,omitempty"`

	// RunDir holds runtime state: sockets, config disks and the record of
	// what is currently running. Expected to be a tmpfs, so that a reboot
	// clears it.
	RunDir string `yaml:"run_dir,omitempty"`

	// API configures where the API is served.
	API APIConfig `yaml:"api"`

	// Resources sets how much CPU and memory instances may be given.
	Resources ResourcesConfig `yaml:"resources"`

	// Network configures host networking.
	Network NetworkConfig `yaml:"network"`

	// Defaults names what an instance gets when it leaves something out.
	Defaults DefaultsConfig `yaml:"defaults"`

	// Metrics configures the Prometheus endpoint. Off unless asked for.
	Metrics MetricsConfig `yaml:"metrics"`

	// Images configures the image store: garbage collection, off unless a
	// limit is set.
	Images ImagesConfig `yaml:"images"`

	// Events configures how much of what happened is kept.
	Events EventsConfig `yaml:"events"`

	// LogLevel is debug, info, warn or error.
	LogLevel string `yaml:"log_level,omitempty"`
}

// APIConfig controls where the API is served. It is always served on a Unix
// socket, and on TCP too if asked.
type APIConfig struct {
	// Socket is the local transport. Access control is the socket's file
	// permissions: whoever can open it may do anything.
	Socket SocketConfig `yaml:"socket"`

	// TCP is the network transport. Every connection is mutual TLS, and
	// only clients enrolled with a token from 'dicer token create' are served.
	TCP TCPConfig `yaml:"tcp"`
}

// SocketConfig controls the API socket.
type SocketConfig struct {
	// Path is where the socket is created.
	Path string `yaml:"path,omitempty"`

	// Mode is the socket's permission bits.
	Mode uint32 `yaml:"mode,omitempty"`
}

// TCPConfig controls the network listener. Off unless Listen is set.
type TCPConfig struct {
	// Listen is the host:port to serve on.
	Listen string `yaml:"listen,omitempty"`

	// Advertise are the host:port pairs clients should connect to, written
	// into every enrolment token. Worked out from Listen and the host's
	// addresses when empty, which is wrong behind NAT or a port forward.
	Advertise []string `yaml:"advertise,omitempty"`
}

// Enabled reports whether the API is served over TCP.
func (t TCPConfig) Enabled() bool {
	return t.Listen != ""
}

// ResourcesConfig sets how much of the host instances may be given. A start
// that would take more is refused.
//
// What instances may be given is the host's CPUs times CPUOvercommit, and
// its memory less ReservedMemoryBytes, times MemoryOvercommit.
type ResourcesConfig struct {
	// CPUOvercommit is how many vCPUs may share each of the host's CPUs.
	// A vCPU is a thread, and an idle one costs nothing, so the default
	// lets four share each.
	CPUOvercommit float64 `yaml:"cpu_overcommit,omitempty"`

	// MemoryOvercommit scales the memory instances may be given. The
	// default, 1, gives out no more than there is: nothing takes memory
	// back from a guest that uses it, and a host that runs out kills VMs.
	MemoryOvercommit float64 `yaml:"memory_overcommit,omitempty"`

	// ReservedMemoryBytes is kept back for the host itself and for the
	// VMMs' own overhead above their guests' memory.
	ReservedMemoryBytes int64 `yaml:"reserved_memory_bytes,omitempty"`
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
func (r *ResourcesConfig) capacity(cpus int, memoryBytes int64) (dicer.Capacity, error) {
	if r.ReservedMemoryBytes >= memoryBytes {
		return dicer.Capacity{}, fmt.Errorf(
			"resources.reserved_memory_bytes (%d) leaves nothing of the host's %d bytes of memory for instances",
			r.ReservedMemoryBytes, memoryBytes)
	}

	return dicer.Capacity{
		Host:                dicer.Resources{VCPUs: cpus, MemoryBytes: memoryBytes},
		ReservedMemoryBytes: r.ReservedMemoryBytes,
		CPUOvercommit:       r.CPUOvercommit,
		MemoryOvercommit:    r.MemoryOvercommit,
	}, nil
}

// NetworkConfig controls host networking.
type NetworkConfig struct {
	// UplinkInterface is the interface NAT traffic leaves by. Detected from
	// the default route when empty.
	UplinkInterface string `yaml:"uplink_interface,omitempty"`

	// UplinkCapacityBps is the uplink's capacity, used as the ceiling for
	// per-instance bandwidth shaping.
	UplinkCapacityBps int64 `yaml:"uplink_capacity_bps,omitempty"`

	// UploadBurstMultiplier and DownloadBurstMultiplier scale an instance's
	// rate limit into the burst it may briefly exceed it by.
	UploadBurstMultiplier   int `yaml:"upload_burst_multiplier,omitempty"`
	DownloadBurstMultiplier int `yaml:"download_burst_multiplier,omitempty"`
}

// DefaultsConfig names the kernel and network an instance gets when its
// definition names none. Either may be left unset, and then an instance gets
// the only kernel, or network, there is -- and must name one if there are
// several. They are looked up as each instance is created, so they may name
// a kernel or network that does not exist yet.
type DefaultsConfig struct {
	// Kernel is the default kernel's name.
	Kernel string `yaml:"kernel,omitempty"`

	// Network is the default network's name.
	Network string `yaml:"network,omitempty"`
}

// validate reports whether the defaults are names a kernel and network
// could have.
func (d *DefaultsConfig) validate() error {
	if d.Kernel != "" {
		if err := dicer.ValidateName(d.Kernel); err != nil {
			return fmt.Errorf("defaults.kernel: %w", err)
		}
	}
	if d.Network != "" {
		if err := dicer.ValidateName(d.Network); err != nil {
			return fmt.Errorf("defaults.network: %w", err)
		}
	}
	return nil
}

// MetricsConfig controls the Prometheus endpoint.
//
// It configures serving, not collecting: the daemon records its metrics
// either way, and this decides whether anything can read them. Serving is off
// by default because it has nothing to authenticate with: unlike the API's
// TCP listener, it serves anyone who connects. So it binds to loopback unless
// configured otherwise; exposing it beyond the host is a decision, not a
// default.
type MetricsConfig struct {
	// Enabled serves the endpoint.
	Enabled bool `yaml:"enabled,omitempty"`

	// Listen is the host:port to serve on.
	Listen string `yaml:"listen,omitempty"`

	// Path is the URL the metrics are served at. Any other path is a 404.
	Path string `yaml:"path,omitempty"`
}

// ImagesConfig configures the image store.
//
// Garbage collection removes images nothing uses -- no instance is defined
// to boot from them, no guest runs on them, no snapshot needs them -- once
// they have gone unused for gc_max_unused_age, and while the store is larger
// than gc_max_size, the least recently used first. With neither set, images
// are only ever removed by a prune.
type ImagesConfig struct {
	// GCMaxUnusedAge is how long an image may go unused before it is
	// removed, as a Go duration: 168h. Zero means no limit.
	GCMaxUnusedAge time.Duration `yaml:"gc_max_unused_age,omitempty"`

	// GCMaxSize is what the image store -- the bootable disks and the
	// layer cache -- may occupy: 50GiB. Zero means no limit.
	GCMaxSize byteSize `yaml:"gc_max_size,omitempty"`

	// GCInterval is how often garbage collection runs. Defaults to an
	// hour.
	GCInterval time.Duration `yaml:"gc_interval,omitempty"`
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

// byteSize is a size in bytes, written in YAML as a number of bytes or with
// a unit, as the CLI takes sizes: 50GiB, 512MiB.
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

// EventsConfig configures the events log: what happened to the instances
// and images on this host, kept in the data directory for `dicer events` and
// `dicer inspect`.
type EventsConfig struct {
	// MaxCount is how many events are kept, the most recent. Defaults to
	// 10000.
	MaxCount int `yaml:"max_count,omitempty"`

	// MaxAge is how long an event is kept, as a Go duration: 720h. Zero
	// means no limit.
	MaxAge time.Duration `yaml:"max_age,omitempty"`
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
		if len(a.TCP.Advertise) > 0 {
			return errors.New("api.tcp.advertise is set but api.tcp.listen is not")
		}
		return nil
	}

	if _, _, err := net.SplitHostPort(a.TCP.Listen); err != nil {
		return fmt.Errorf("invalid api.tcp.listen %q: want host:port: %w", a.TCP.Listen, err)
	}
	for _, addr := range a.TCP.Advertise {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("invalid api.tcp.advertise address %q: want host:port: %w", addr, err)
		}
	}

	return nil
}

// validate reports whether the metrics configuration is usable. A disabled
// endpoint is never invalid: nothing is going to read the fields.
func (m *MetricsConfig) validate() error {
	if !m.Enabled {
		return nil
	}

	if m.Listen == "" {
		return errors.New("metrics.listen is required when metrics are enabled")
	}
	if _, _, err := net.SplitHostPort(m.Listen); err != nil {
		return fmt.Errorf("invalid metrics.listen %q: want host:port: %w", m.Listen, err)
	}
	if !strings.HasPrefix(m.Path, "/") {
		return fmt.Errorf("invalid metrics.path %q: want a path beginning with /", m.Path)
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

// defaultConfig returns the configuration used when no file is present and as
// the base that a config file overrides.
func defaultConfig() Config {
	return Config{
		DataDir: filestore.DefaultDataDir,
		RunDir:  vm.DefaultRunDir,
		API: APIConfig{
			Socket: SocketConfig{Path: defaultSocketPath, Mode: defaultSocketMode},
		},
		Resources: ResourcesConfig{
			CPUOvercommit:       defaultCPUOvercommit,
			MemoryOvercommit:    defaultMemoryOvercommit,
			ReservedMemoryBytes: defaultReservedMemoryBytes,
		},
		LogLevel: "info",
		Metrics: MetricsConfig{
			Listen: defaultMetricsListen,
			Path:   defaultMetricsPath,
		},
		Images: ImagesConfig{GCInterval: defaultGCInterval},
		Events: EventsConfig{MaxCount: events.DefaultMaxCount},
	}
}

// loadConfig reads the configuration file. A missing file is not an error:
// the defaults are a working single-host configuration, so the daemon should
// start without one.
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

	// Unknown keys are errors, not ignored: a misspelt key would otherwise quietly leave its setting at the
	// default -- a daemon on the wrong socket, say, with nothing to explain
	// why.
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
