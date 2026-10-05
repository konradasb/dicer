// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/naming"
)

const (
	// ConfigDirEnv is the environment variable that overrides the
	// configuration directory.
	ConfigDirEnv = "DICER_CONFIG_DIR"

	remotesFile = "remotes.yaml"
)

// Config is the set of remotes, as kept in remotes.yaml.
type Config struct {
	// Current is the remote commands go to unless told otherwise. Empty
	// means the local one.
	Current string `yaml:"current,omitempty"`

	// Remotes are the configured remotes by name, the built-in one aside.
	Remotes map[string]Remote `yaml:"remotes,omitempty"`
}

// ConfigDir returns the configuration directory: $DICER_CONFIG_DIR, or dicer
// under the user's configuration directory.
func ConfigDir() (string, error) {
	if dir := os.Getenv(ConfigDirEnv); dir != "" {
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

	path := filepath.Join(dir, remotesFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read remotes: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
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

// Remote returns a remote by name, the built-in one included. A name not
// configured is an errdefs.ErrNotFound.
func (c *Config) Remote(name string) (Remote, error) {
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
	if _, err := c.Remote(name); err != nil {
		return err
	}

	c.Current = name
	if name == Local {
		c.Current = ""
	}

	return nil
}
