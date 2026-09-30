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
	"os/exec"
	"os/user"
	"strconv"
	"time"

	"github.com/docker/go-units"
	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer/internal/defaults"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/hostnet"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/registry"
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

	// The keepalive defaults leave room for a client's default interval,
	// dicer.DefaultKeepaliveInterval, above the daemon's minimum.
	defaultKeepaliveInterval          = 30 * time.Second
	defaultKeepaliveTimeout           = 10 * time.Second
	defaultKeepaliveMinClientInterval = 10 * time.Second
)

// Config is the daemon configuration. Every field's doc comment is its entry
// in the configuration reference, which make docs-gen generates from them:
// write them for someone configuring the daemon.
type Config struct {
	// DataDir is where the daemon keeps what persists: the definitions of
	// instances, networks, volumes and kernels, the images, and the
	// instances' disks. Unset is /var/lib/dicer.
	DataDir string `yaml:"data_dir,omitempty"`

	// RunDir is where the daemon keeps runtime state: sockets, config disks
	// and the record of what is running. It should be a tmpfs, so that a
	// reboot clears it. Unset is /run/dicer.
	RunDir string `yaml:"run_dir,omitempty"`

	// API is where the API is served: always on a Unix socket, and on TCP
	// too if api.tcp.listen is set.
	API APIConfig `yaml:"api"`

	// Resources is how much CPU and memory instances may be given, all
	// together: the host's CPUs times cpu_overcommit, and its memory less
	// reserved_memory_bytes times memory_overcommit. A start that would take
	// more is refused.
	Resources ResourcesConfig `yaml:"resources"`

	// Network is the host's networking.
	Network NetworkConfig `yaml:"network"`

	// Defaults is what an instance gets when its definition leaves something
	// out.
	Defaults DefaultsConfig `yaml:"defaults"`

	// Metrics is the Prometheus endpoint, served at `/metrics` without
	// authentication. Metrics are always recorded: this decides only whether
	// they are served.
	Metrics MetricsConfig `yaml:"metrics"`

	// Images is image garbage collection, which removes only images no
	// instance, running guest or snapshot uses. It is off while
	// gc_max_unused_age and gc_max_size are both unset.
	Images ImagesConfig `yaml:"images"`

	// Events bounds the events log that dicer events shows.
	Events EventsConfig `yaml:"events"`

	// Registries are the credentials for private registries, by host as an
	// image's name gives it: docker.io, ghcr.io, or a registry's host:port.
	// Images from any other registry are pulled anonymously.
	//
	//	registries:
	//	  ghcr.io:
	//	    username: dicer-bot
	//	    password_file: /etc/dicerd/secrets/ghcr-token
	Registries map[string]RegistryConfig `yaml:"registries,omitempty"`

	// LogLevel is debug, info, warn or error. Unset is info.
	LogLevel string `yaml:"log_level,omitempty"`
}

// RegistryConfig is how the daemon logs in to one registry: with a username
// and a password, given or read from a file, or through a credential helper.
type RegistryConfig struct {
	// Username is the user to log in as.
	Username string `yaml:"username,omitempty"`

	// Password is the password or token to log in with, and PasswordFile a
	// file holding it, readable only by root. Give one or the other.
	Password     string `yaml:"password,omitempty"`
	PasswordFile string `yaml:"password_file,omitempty"`

	// CredentialHelper names a `docker-credential-<name>` program on the
	// daemon's PATH, such as ecr-login, that gives the credentials instead of
	// username and password: for a registry whose tokens expire.
	CredentialHelper string `yaml:"credential_helper,omitempty"`
}

// validate reports whether the registry's credentials are complete. The files
// and programs they name are checkHost's.
func (r RegistryConfig) validate(host string) error {
	key := "registries." + host
	switch {
	case r.CredentialHelper != "" && (r.Username != "" || r.Password != "" || r.PasswordFile != ""):
		return fmt.Errorf("%s: credential_helper gives the credentials itself: leave out username and password", key)
	case r.CredentialHelper != "":
		return nil
	case r.Username == "":
		return fmt.Errorf("%s: username is required, or a credential_helper", key)
	case r.Password != "" && r.PasswordFile != "":
		return fmt.Errorf("%s: set password or password_file, not both", key)
	case r.Password == "" && r.PasswordFile == "":
		return fmt.Errorf("%s: password or password_file is required", key)
	}
	return nil
}

// checkHost reports whether what the registry's credentials name is on this
// host: the password file readable, or the credential helper on PATH.
func (r RegistryConfig) checkHost(host string) error {
	key := "registries." + host
	if r.PasswordFile != "" {
		if _, err := os.ReadFile(r.PasswordFile); err != nil {
			return fmt.Errorf("%s.password_file: %w", key, err)
		}
	}
	if r.CredentialHelper != "" {
		if _, err := exec.LookPath("docker-credential-" + r.CredentialHelper); err != nil {
			return fmt.Errorf("%s.credential_helper: %w", key, err)
		}
	}
	return nil
}

// auth is the registry's credentials as the registry client takes them.
func (r RegistryConfig) auth() registry.Auth {
	return registry.Auth{
		Username:         r.Username,
		Password:         r.Password,
		PasswordFile:     r.PasswordFile,
		CredentialHelper: r.CredentialHelper,
	}
}

// APIConfig controls where the API is served: always on a Unix socket, and
// on TCP if Listen is set.
type APIConfig struct {
	// Socket is the local Unix socket. Anyone who can open it has full
	// control of the daemon.
	Socket SocketConfig `yaml:"socket"`

	// TCP is the network listener. Without tls it is unauthenticated and
	// unencrypted: anyone who can reach it has root-equivalent access to
	// this host.
	TCP TCPConfig `yaml:"tcp"`

	// Keepalive is how the daemon finds clients that have gone without
	// closing their connection, because their host lost power or the network
	// between dropped, and how often clients may check the same of it. Such
	// a connection otherwise lasts until the kernel gives up on it, which
	// can take a quarter of an hour.
	Keepalive KeepaliveConfig `yaml:"keepalive"`
}

// KeepaliveConfig is how the daemon and its clients check that the other is
// still there.
type KeepaliveConfig struct {
	// Interval is how long a connection may carry nothing before the daemon
	// pings the client. Unset is 30s.
	Interval time.Duration `yaml:"interval,omitempty"`

	// Timeout is how long the daemon waits for the answer to a ping before
	// closing the connection. Unset is 10s.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// MinClientInterval is how often a client may ping the daemon at most,
	// even with no call in flight. A client pinging more often is
	// disconnected. Dicer's clients ping every 30s unless told otherwise.
	// Unset is 10s.
	MinClientInterval time.Duration `yaml:"min_client_interval,omitempty"`
}

func (k *KeepaliveConfig) validate() error {
	switch {
	case k.Interval <= 0:
		return errors.New("api.keepalive.interval must be positive")
	case k.Timeout <= 0:
		return errors.New("api.keepalive.timeout must be positive")
	case k.MinClientInterval <= 0:
		return errors.New("api.keepalive.min_client_interval must be positive")
	}
	return nil
}

// SocketConfig controls the API socket. Access is controlled by its file
// permissions.
type SocketConfig struct {
	// Path is the socket's path. Unset is /run/dicer/dicer.sock.
	Path string `yaml:"path,omitempty"`

	// Mode is the socket's permission bits. Unset is 0660.
	Mode uint32 `yaml:"mode,omitempty"`

	// Group is the group the socket belongs to, by name or ID. Its members
	// can use the API as far as mode lets the group, which is as much as root
	// on this host. Unset leaves the socket root's alone.
	Group string `yaml:"group,omitempty"`
}

// gid returns the ID of the socket's group, or -1 if it names none.
func (s SocketConfig) gid() (int, error) {
	if s.Group == "" {
		return -1, nil
	}

	if id, err := strconv.Atoi(s.Group); err == nil {
		return id, nil
	}
	g, err := user.LookupGroup(s.Group)
	if err != nil {
		return 0, fmt.Errorf("api.socket.group: %w", err)
	}
	id, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("api.socket.group %s has the ID %q, not a number", s.Group, g.Gid)
	}
	return id, nil
}

// TCPConfig controls the network listener. Without TLS it is
// unauthenticated and unencrypted.
type TCPConfig struct {
	// Listen is the host:port to serve on, such as 0.0.0.0:7443. Unset
	// serves no listener.
	Listen string `yaml:"listen,omitempty"`

	// TLS is the listener's TLS.
	TLS TLSConfig `yaml:"tls"`
}

// TLSConfig configures TLS on the network listener. The certificate and key
// are reloaded when they change on disk; the client CA is read at startup.
type TLSConfig struct {
	// CertFile is the daemon's PEM certificate, which must name the address
	// clients connect to. It is reloaded when it changes on disk.
	CertFile string `yaml:"cert_file,omitempty"`

	// KeyFile is the PEM private key for CertFile. It is reloaded when it
	// changes on disk.
	KeyFile string `yaml:"key_file,omitempty"`

	// ClientCAFile holds the PEM authorities client certificates must be
	// issued by. Set, every client must present a valid certificate; unset,
	// any client is accepted. Use a CA dedicated to Dicer's clients. It is
	// read at startup.
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
	// CPUOvercommit is how many vCPUs instances may be given per host CPU.
	// Unset is 4.
	CPUOvercommit float64 `yaml:"cpu_overcommit,omitempty"`

	// MemoryOvercommit multiplies the memory available to instances: 1 never
	// promises more memory than the host has. Unset is 1.
	MemoryOvercommit float64 `yaml:"memory_overcommit,omitempty"`

	// ReservedMemoryBytes is the memory, in bytes, kept back for the host and
	// the hypervisors' overhead. Unset is 1073741824, 1 GiB.
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
	// UplinkInterface is the interface NAT traffic leaves by. Unset detects
	// it from the default route.
	UplinkInterface string `yaml:"uplink_interface,omitempty"`

	// UplinkCapacityBps is the uplink's capacity in bits per second, the
	// ceiling for per-instance bandwidth shaping. Unset sets no ceiling.
	UplinkCapacityBps int64 `yaml:"uplink_capacity_bps,omitempty"`

	// UploadBurstMultiplier is how far an instance may briefly exceed its
	// upload rate limit, as a multiple of it. Unset is 4.
	UploadBurstMultiplier int `yaml:"upload_burst_multiplier,omitempty"`

	// DownloadBurstMultiplier is how far an instance may briefly exceed its
	// download rate limit, as a multiple of it. Unset is 4.
	DownloadBurstMultiplier int `yaml:"download_burst_multiplier,omitempty"`
}

// DefaultsConfig names the kernel and network an instance gets when it names
// none. Unset, an instance gets the only one there is.
type DefaultsConfig struct {
	// Kernel is the kernel an instance boots when it names none. Unset is
	// the only kernel, if there is exactly one.
	Kernel string `yaml:"kernel,omitempty"`

	// Network is the network an instance is attached to when it names none.
	// Unset is the only network, if there is exactly one.
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
	// Enable serves the endpoint.
	Enable bool `yaml:"enable,omitempty"`

	// Listen is the host:port the endpoint is served on. Unset is
	// 127.0.0.1:9101.
	Listen string `yaml:"listen,omitempty"`
}

// ImagesConfig configures image garbage collection, which removes unused
// images older than GCMaxUnusedAge, and the least recently used while the
// store exceeds GCMaxSize. Zero limits disable it.
type ImagesConfig struct {
	// GCMaxUnusedAge removes images unused for longer than this, such as
	// 168h. Unset is no limit.
	GCMaxUnusedAge time.Duration `yaml:"gc_max_unused_age,omitempty"`

	// GCMaxSize removes the least recently used images while the store is
	// larger than this, such as 50GiB. Unset is no limit.
	GCMaxSize byteSize `yaml:"gc_max_size,omitempty"`

	// GCInterval is how often garbage collection runs. Unset is 1h.
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
	// MaxCount is how many of the most recent events are kept. Unset is
	// 10000.
	MaxCount int `yaml:"max_count,omitempty"`

	// MaxAge drops events older than this, such as 720h. Unset is no limit.
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
	for host, r := range c.Registries {
		if err := r.validate(host); err != nil {
			return err
		}
	}

	return c.Metrics.validate()
}

// validate reports whether the API configuration is usable.
func (a *APIConfig) validate() error {
	if a.Socket.Path == "" {
		return errors.New("api.socket.path is required")
	}
	if _, err := a.Socket.gid(); err != nil {
		return err
	}
	if err := a.Keepalive.validate(); err != nil {
		return err
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
			Keepalive: KeepaliveConfig{
				Interval:          defaultKeepaliveInterval,
				Timeout:           defaultKeepaliveTimeout,
				MinClientInterval: defaultKeepaliveMinClientInterval,
			},
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
