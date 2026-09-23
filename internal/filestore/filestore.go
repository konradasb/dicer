// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package filestore stores the instance, network, volume and kernel
// definitions as YAML files, cached in memory and written through:
//
//	/var/lib/dicer/instances/<name>/config.yaml
//	/var/lib/dicer/networks/<name>.yaml
//	/var/lib/dicer/volumes/<name>.yaml
//	/var/lib/dicer/kernels/<name>.yaml
package filestore

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/defaults"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/types"
)

// Config configures a Manager.
type Config struct {
	DataDir string
	Logger  *slog.Logger
}

// Manager holds the definitions, backed by YAML files.
//
// It is the implementation of vm.Definitions.
type Manager struct {
	dataDir string
	logger  *slog.Logger

	instances *collection[types.InstanceSpec]
	networks  *collection[types.Network]
	volumes   *collection[types.Volume]
	kernels   *collection[types.Kernel]
}

// NewManager loads all definitions into memory, creating the data directory
// if needed. Malformed files are logged and skipped.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = defaults.DataDir
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	logger := cfg.Logger.With("component", "filestore")
	m := &Manager{dataDir: cfg.DataDir, logger: logger}

	for _, dir := range []string{instancesDir, networksDir, volumesDir, kernelsDir} {
		path := filepath.Join(cfg.DataDir, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
	}

	m.instances = newCollection(
		"instance", filepath.Join(cfg.DataDir, instancesDir), nested, logger,
		func(v types.InstanceSpec) (string, string) { return v.ID, v.Name },
	)
	m.networks = newCollection(
		"network", filepath.Join(cfg.DataDir, networksDir), flat, logger,
		func(v types.Network) (string, string) { return v.ID, v.Name },
	)
	m.volumes = newCollection(
		"volume", filepath.Join(cfg.DataDir, volumesDir), flat, logger,
		func(v types.Volume) (string, string) { return v.ID, v.Name },
	)
	m.kernels = newCollection(
		"kernel", filepath.Join(cfg.DataDir, kernelsDir), flat, logger,
		func(v types.Kernel) (string, string) { return v.ID, v.Name },
	)
	for _, coll := range []interface{ load() error }{
		m.instances, m.networks, m.volumes, m.kernels,
	} {
		if err := coll.load(); err != nil {
			return nil, err
		}
	}

	logger.Info("definitions loaded",
		"data_dir", cfg.DataDir,
		"instances", m.instances.len(),
		"networks", m.networks.len(),
		"volumes", m.volumes.len(),
		"kernels", m.kernels.len(),
	)

	return m, nil
}

const (
	instancesDir = "instances"
	networksDir  = "networks"
	volumesDir   = "volumes"
	kernelsDir   = "kernels"

	configFile = "config.yaml"
)

// layout describes how a collection maps names to paths.
type layout int

const (
	// flat stores each resource as <dir>/<name>.yaml.
	flat layout = iota
	// nested stores each resource as <dir>/<name>/config.yaml, leaving room
	// for sibling files -- an instance's overlay disk lives next to its
	// definition and is removed with it.
	nested
)

// collection is a set of resources backed by YAML files, looked up by name or
// ID.
type collection[T any] struct {
	mu     sync.RWMutex
	kind   string // what it holds, as an error names it: "instance"
	dir    string
	layout layout
	logger *slog.Logger
	items  map[string]T      // by name
	byID   map[string]string // id -> name
	keyOf  func(T) (id, name string)
}

func newCollection[T any](
	kind, dir string, l layout, logger *slog.Logger, keyOf func(T) (string, string),
) *collection[T] {
	return &collection[T]{
		kind:   kind,
		dir:    dir,
		layout: l,
		logger: logger,
		items:  make(map[string]T),
		byID:   make(map[string]string),
		keyOf:  keyOf,
	}
}

// path returns the file holding the named resource.
func (c *collection[T]) path(name string) string {
	if c.layout == nested {
		return filepath.Join(c.dir, name, configFile)
	}
	return filepath.Join(c.dir, name+".yaml")
}

// dirFor returns the directory owned by the named resource. For a flat
// collection that is the shared parent, so callers must not remove it.
func (c *collection[T]) dirFor(name string) string {
	if c.layout == nested {
		return filepath.Join(c.dir, name)
	}
	return c.dir
}

// load reads every definition in the collection's directory into memory.
func (c *collection[T]) load() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", c.dir, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, e := range entries {
		var name string
		switch {
		case c.layout == nested && e.IsDir():
			name = e.Name()
		case c.layout == flat && !e.IsDir() && filepath.Ext(e.Name()) == ".yaml":
			name = e.Name()[:len(e.Name())-len(".yaml")]
		default:
			continue
		}

		data, err := os.ReadFile(c.path(name))
		if err != nil {
			c.logger.Warn("skipping unreadable definition", "path", c.path(name), "error", err)
			continue
		}

		var item T
		if err := yaml.Unmarshal(data, &item); err != nil {
			c.logger.Warn("skipping malformed definition", "path", c.path(name), "error", err)
			continue
		}

		// The name keys every lookup and write, so a file that disagrees
		// with its location would be read under one name and written under
		// another.
		id, itemName := c.keyOf(item)
		if itemName != name {
			c.logger.Warn("skipping definition whose name does not match its location",
				"path", c.path(name), "name_in_file", itemName)
			continue
		}

		c.items[name] = item
		if id != "" {
			c.byID[id] = name
		}
	}

	return nil
}

func (c *collection[T]) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// resolve maps a name or ID to a name. It must be called with the lock held.
func (c *collection[T]) resolve(nameOrID string) (string, bool) {
	if _, ok := c.items[nameOrID]; ok {
		return nameOrID, true
	}
	if name, ok := c.byID[nameOrID]; ok {
		return name, true
	}
	return "", false
}

// get returns the resource with the given name or ID.
func (c *collection[T]) get(nameOrID string) (T, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var zero T
	name, ok := c.resolve(nameOrID)
	if !ok {
		return zero, errdefs.NotFound("no %s %q", c.kind, nameOrID)
	}
	return c.items[name], nil
}

// list returns all resources, sorted by name for stable output.
func (c *collection[T]) list() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := make([]string, 0, len(c.items))
	for name := range c.items {
		names = append(names, name)
	}
	slices.Sort(names)

	out := make([]T, 0, len(names))
	for _, name := range names {
		out = append(out, c.items[name])
	}
	return out
}

// create writes a new resource, failing if the name is already taken.
func (c *collection[T]) create(item T) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	id, name := c.keyOf(item)
	if err := naming.Validate(name); err != nil {
		return err
	}
	if _, ok := c.items[name]; ok {
		return errdefs.Exists("%s %q already exists", c.kind, name)
	}

	if err := c.write(name, item); err != nil {
		return err
	}

	c.items[name] = item
	if id != "" {
		c.byID[id] = name
	}
	return nil
}

// update overwrites an existing resource, failing if it does not exist.
func (c *collection[T]) update(item T) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	id, name := c.keyOf(item)
	if _, ok := c.items[name]; !ok {
		return errdefs.NotFound("no %s %q", c.kind, name)
	}

	if err := c.write(name, item); err != nil {
		return err
	}

	c.items[name] = item
	if id != "" {
		c.byID[id] = name
	}
	return nil
}

// rename stores renamed under its new name, moving its directory with it.
func (c *collection[T]) rename(nameOrID string, renamed T) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	from, ok := c.resolve(nameOrID)
	if !ok {
		return errdefs.NotFound("no %s %q", c.kind, nameOrID)
	}

	id, to := c.keyOf(renamed)
	if err := naming.Validate(to); err != nil {
		return err
	}
	if to == from {
		return nil
	}
	if _, taken := c.items[to]; taken {
		return errdefs.Exists("%s %q already exists", c.kind, to)
	}

	// The directory moves first: a rename that fails halfway must leave the
	// resource where its files are, not where they are not. A flat
	// collection has only the file, which the write below puts in place.
	if c.layout == nested {
		if err := os.Rename(c.dirFor(from), c.dirFor(to)); err != nil {
			return fmt.Errorf("rename %s to %s: %w", c.dirFor(from), c.dirFor(to), err)
		}
	}

	if err := c.write(to, renamed); err != nil {
		return err
	}
	if c.layout == flat {
		if err := os.Remove(c.path(from)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", c.path(from), err)
		}
	}

	delete(c.items, from)
	c.items[to] = renamed
	if id != "" {
		c.byID[id] = to
	}

	return nil
}

// delete removes a resource and its backing file. For nested collections the
// whole resource directory goes, taking any sibling files with it.
func (c *collection[T]) delete(nameOrID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	name, ok := c.resolve(nameOrID)
	if !ok {
		return errdefs.NotFound("no %s %q", c.kind, nameOrID)
	}

	id, _ := c.keyOf(c.items[name])

	if c.layout == nested {
		if err := os.RemoveAll(c.dirFor(name)); err != nil {
			return fmt.Errorf("remove %s: %w", c.dirFor(name), err)
		}
	} else if err := os.Remove(c.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", c.path(name), err)
	}

	delete(c.items, name)
	delete(c.byID, id)
	return nil
}

// write persists a single resource atomically. It must be called with the
// lock held.
func (c *collection[T]) write(name string, item T) error {
	data, err := yaml.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshal %q: %w", name, err)
	}

	if c.layout == nested {
		if err := os.MkdirAll(c.dirFor(name), 0o700); err != nil {
			return fmt.Errorf("create %s: %w", c.dirFor(name), err)
		}
	}

	return atomicfile.Write(c.path(name), data, 0o600)
}
