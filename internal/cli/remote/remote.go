// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package remote is the daemons the CLI knows, kept with the current one in
// ~/.config/dicer/remotes.yaml, and how each is reached.
package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/naming"
)

const (
	// Local names the built-in remote: the daemon on this machine, on its
	// default socket. It is always there and cannot be deleted.
	Local = "local"

	// DirEnv overrides the configuration directory.
	DirEnv = "DICER_CONFIG_DIR"

	remotesFile = "remotes.yaml"
)

// Remote is a daemon the CLI talks to.
type Remote struct {
	// Address is a gRPC target: unix:///path/to/socket, or HOST:PORT for a
	// daemon's TCP listener.
	Address string `yaml:"address"`

	// TLS is what a TCP address is reached with. Nil is plaintext.
	TLS *TLS `yaml:"tls,omitempty"`
}

// IsAddress reports whether s is an address rather than a remote's name,
// which holds neither ':' nor '/'.
func IsAddress(s string) bool {
	return strings.ContainsAny(s, ":/")
}

// Parse returns the remote an address names, with no TLS.
func Parse(address string) (Remote, error) {
	r := Remote{Address: address}
	if err := r.Validate(); err != nil {
		return Remote{}, err
	}

	return r, nil
}

// Validate reports whether the remote can be connected to. The TLS files are
// not read.
func (r Remote) Validate() error {
	if path, ok := strings.CutPrefix(r.Address, socketScheme); ok {
		switch {
		case !filepath.IsAbs(path):
			return errdefs.InvalidArgument("invalid address %q: the socket path must be absolute", r.Address)
		case r.TLS != nil:
			return errdefs.InvalidArgument(
				"a unix:// remote cannot have tls: a socket is controlled by its file permissions")
		}
		return nil
	}

	if _, _, err := net.SplitHostPort(strings.TrimPrefix(r.Address, "dns:///")); err != nil {
		return errdefs.InvalidArgument("invalid address %q: want %s/PATH or HOST:PORT", r.Address, socketScheme)
	}
	return r.TLS.Validate()
}

// ClientOptions returns what dicer.NewClient needs to reach the remote.
func (r Remote) ClientOptions() ([]dicer.Option, error) {
	opts := []dicer.Option{dicer.WithAddress(r.Address)}
	if r.TLS != nil {
		cfg, err := r.TLS.Config()
		if err != nil {
			return nil, err
		}
		opts = append(opts, dicer.WithTLS(cfg))
	}

	return opts, nil
}

// socketScheme is what the address of a daemon's socket starts with.
const socketScheme = "unix://"

// Config is the set of remotes, as kept in remotes.yaml.
type Config struct {
	// Current is the remote commands go to unless told otherwise. Empty
	// means the local one.
	Current string `yaml:"current,omitempty"`

	// Remotes are the configured remotes by name, the built-in one aside.
	Remotes map[string]Remote `yaml:"remotes,omitempty"`
}

// Dir returns the configuration directory: $DICER_CONFIG_DIR, or dicer under
// the user's configuration directory.
func Dir() (string, error) {
	if dir := os.Getenv(DirEnv); dir != "" {
		return dir, nil
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the configuration directory: %w", err)
	}

	return filepath.Join(base, "dicer"), nil
}

// Load reads the remotes kept in dir. No file means none but the local one.
func Load(dir string) (*Config, error) {
	cfg := &Config{Remotes: make(map[string]Remote)}

	data, err := os.ReadFile(filepath.Join(dir, remotesFile))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read remotes: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, remotesFile), err)
	}
	if cfg.Remotes == nil {
		cfg.Remotes = make(map[string]Remote)
	}

	return cfg, nil
}

// Save writes the remotes into dir.
func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal remotes: %w", err)
	}

	return atomicfile.Write(filepath.Join(dir, remotesFile), data, 0o600)
}

// Get returns a remote by name, the built-in one included.
func (c *Config) Get(name string) (Remote, error) {
	if name == Local {
		return Remote{Address: dicer.DefaultAddress}, nil
	}

	r, ok := c.Remotes[name]
	if !ok {
		return Remote{}, errdefs.NotFound("no remote %q", name)
	}

	return r, nil
}

// CurrentName returns the name of the current remote.
func (c *Config) CurrentName() string {
	if c.Current == "" {
		return Local
	}

	return c.Current
}

// Names returns every remote's name, the built-in one included, sorted.
func (c *Config) Names() []string {
	names := append([]string{Local}, slices.Collect(maps.Keys(c.Remotes))...)
	slices.Sort(names)

	return names
}

// Create adds a remote.
func (c *Config) Create(name string, r Remote) error {
	if err := naming.Validate(name); err != nil {
		return err
	}
	if name == Local {
		return errdefs.InvalidArgument("%q is the built-in remote for this machine; choose another name", Local)
	}
	if _, ok := c.Remotes[name]; ok {
		return errdefs.Exists("remote %q already exists", name)
	}
	if err := r.Validate(); err != nil {
		return err
	}

	c.Remotes[name] = r

	return nil
}

// Delete removes a remote. Deleting the current one makes the local one
// current.
func (c *Config) Delete(name string) error {
	if name == Local {
		return errdefs.InvalidArgument("%q is the built-in remote for this machine, and cannot be deleted", Local)
	}
	if _, ok := c.Remotes[name]; !ok {
		return errdefs.NotFound("no remote %q", name)
	}

	delete(c.Remotes, name)
	if c.Current == name {
		c.Current = ""
	}

	return nil
}

// Use makes a remote the current one.
func (c *Config) Use(name string) error {
	if _, err := c.Get(name); err != nil {
		return err
	}

	c.Current = name
	if name == Local {
		c.Current = ""
	}

	return nil
}
