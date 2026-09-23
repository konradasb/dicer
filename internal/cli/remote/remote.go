// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package remote keeps the daemons the dicer CLI knows how to reach, and the
// key it identifies itself to them with.
//
// Both live in the user's configuration directory, ~/.config/dicer:
//
//	remotes.yaml       the remotes, and which one is current
//	client/cert.pem    this user's certificate, and
//	client/key.pem     its key, which never leaves the machine
//
// One key serves every remote, as one SSH key serves every host: each daemon
// trusts it separately, and deleting a remote here does not stop a daemon
// trusting it -- that is 'dicer client delete', run against the daemon.
//
// What a remote is, and how one is connected to, belongs to the client in the
// root package. This is only where the CLI writes them down: a program built
// on the client keeps its remotes however it likes, and a library that read a
// user's configuration files behind its back would be a surprising one.
package remote

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/atomicfile"
)

const (
	// Local names the built-in remote: the daemon on this machine, on its
	// default socket. It is always there and cannot be deleted.
	Local = "local"

	// DirEnv overrides the configuration directory.
	DirEnv = "DICER_CONFIG_DIR"

	remotesFile = "remotes.yaml"
	clientDir   = "client"
)

// Config is the set of remotes, as kept in remotes.yaml.
type Config struct {
	// Current is the remote commands go to unless told otherwise. Empty
	// means the local one.
	Current string `yaml:"current,omitempty"`

	// Remotes are the configured remotes by name, the built-in one aside.
	Remotes map[string]dicer.Remote `yaml:"remotes,omitempty"`
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
	cfg := &Config{Remotes: make(map[string]dicer.Remote)}

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
		cfg.Remotes = make(map[string]dicer.Remote)
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
func (c *Config) Get(name string) (dicer.Remote, error) {
	if name == Local {
		return dicer.LocalRemote(), nil
	}

	r, ok := c.Remotes[name]
	if !ok {
		return dicer.Remote{}, dicer.NotFound("no remote %q", name)
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
func (c *Config) Create(name string, r dicer.Remote) error {
	if err := dicer.ValidateName(name); err != nil {
		return err
	}
	if name == Local {
		return dicer.InvalidArgument("%q is the built-in remote for this machine; choose another name", Local)
	}
	if _, ok := c.Remotes[name]; ok {
		return dicer.Exists("remote %q already exists", name)
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
		return dicer.InvalidArgument("%q is the built-in remote for this machine, and cannot be deleted", Local)
	}
	if _, ok := c.Remotes[name]; !ok {
		return dicer.NotFound("no remote %q", name)
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

// Identity returns this user's key and certificate, kept in dir, generating
// them on first use.
func Identity(dir string) (tls.Certificate, error) {
	return dicer.LoadIdentity(filepath.Join(dir, clientDir))
}
