// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package filestore

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/naming"
)

// configFile is the file holding a definition in a nested collection.
const configFile = "config.yaml"

// layout describes how a collection maps names to paths.
type layout int

const (
	// flat stores each definition as <dir>/<name>.yaml.
	flat layout = iota
	// nested stores each definition as <dir>/<name>/config.yaml, leaving
	// room for sibling files: an instance's overlay disk lives next to its
	// definition and is removed with it.
	nested
)

// collection is a set of definitions backed by YAML files, looked up by name
// or ID. It is safe for concurrent use.
type collection[T any] struct {
	kind      string // what it holds, as an error names it: "instance"
	dir       string
	layout    layout
	logger    *slog.Logger
	idAndName func(T) (id, name string)

	mu     sync.RWMutex
	byName map[string]T
	byID   map[string]string // id -> name
}

func newCollection[T any](
	kind, dir string, l layout, logger *slog.Logger, idAndName func(T) (id, name string),
) *collection[T] {
	return &collection[T]{
		kind:      kind,
		dir:       dir,
		layout:    l,
		logger:    logger,
		idAndName: idAndName,
		byName:    make(map[string]T),
		byID:      make(map[string]string),
	}
}

// path returns the file holding the named definition.
func (c *collection[T]) path(name string) string {
	if c.layout == nested {
		return filepath.Join(c.dir, name, configFile)
	}
	return filepath.Join(c.dir, name+".yaml")
}

// nestedDir returns the directory a nested collection keeps the named
// definition and its sibling files in.
func (c *collection[T]) nestedDir(name string) string {
	return filepath.Join(c.dir, name)
}

// load creates the collection's directory if needed and reads every
// definition in it into memory. Unreadable, malformed and misplaced
// definitions are logged and skipped.
func (c *collection[T]) load() error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", c.dir, err)
	}
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
			name = strings.TrimSuffix(e.Name(), ".yaml")
		default:
			continue
		}

		path := c.path(name)
		data, err := os.ReadFile(path)
		if err != nil {
			c.logger.Warn("skipping unreadable definition", "path", path, "error", err)
			continue
		}

		var definition T
		if err := yaml.Unmarshal(data, &definition); err != nil {
			c.logger.Warn("skipping malformed definition", "path", path, "error", err)
			continue
		}

		// The name keys every lookup and write, so a file that disagrees
		// with its location would be read under one name and written under
		// another.
		id, nameInFile := c.idAndName(definition)
		if nameInFile != name {
			c.logger.Warn("skipping definition whose name does not match its location",
				"path", path, "name_in_file", nameInFile)
			continue
		}

		c.cache(id, name, definition)
	}

	return nil
}

// cache keeps definition in memory under its name and ID. It must be called
// with the lock held.
func (c *collection[T]) cache(id, name string, definition T) {
	c.byName[name] = definition
	if id != "" {
		c.byID[id] = name
	}
}

// len returns the number of definitions.
func (c *collection[T]) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byName)
}

// nameOf returns the name of the definition with the given name or ID, and
// whether there is one. It must be called with the lock held.
func (c *collection[T]) nameOf(nameOrID string) (string, bool) {
	if _, ok := c.byName[nameOrID]; ok {
		return nameOrID, true
	}
	name, ok := c.byID[nameOrID]
	return name, ok
}

// definition returns the definition with the given name or ID, or an
// errdefs.ErrNotFound error if there is none.
func (c *collection[T]) definition(nameOrID string) (T, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	name, ok := c.nameOf(nameOrID)
	if !ok {
		var zero T
		return zero, errdefs.NotFound("no %s %q", c.kind, nameOrID)
	}
	return c.byName[name], nil
}

// matchingDefinitions returns the definitions match reports true for, in no
// particular order: filtering where they are kept copies only those, and
// sorts none.
func (c *collection[T]) matchingDefinitions(match func(T) bool) []T {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var out []T
	for _, v := range c.byName {
		if match(v) {
			out = append(out, v)
		}
	}
	return out
}

// definitions returns every definition, sorted by name for stable output.
func (c *collection[T]) definitions() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := slices.Sorted(maps.Keys(c.byName))
	out := make([]T, 0, len(names))
	for _, name := range names {
		out = append(out, c.byName[name])
	}
	return out
}

// create writes a new definition, failing if its name is already taken.
func (c *collection[T]) create(definition T) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	id, name := c.idAndName(definition)
	if err := naming.Validate(name); err != nil {
		return err
	}
	if _, ok := c.byName[name]; ok {
		return errdefs.Exists("%s %q already exists", c.kind, name)
	}

	if err := c.write(name, definition); err != nil {
		return err
	}
	c.cache(id, name, definition)
	return nil
}

// update overwrites an existing definition, failing if there is none by its
// name.
func (c *collection[T]) update(definition T) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	id, name := c.idAndName(definition)
	if _, ok := c.byName[name]; !ok {
		return errdefs.NotFound("no %s %q", c.kind, name)
	}

	if err := c.write(name, definition); err != nil {
		return err
	}
	c.cache(id, name, definition)
	return nil
}

// rename stores renamed under its new name, moving its directory with it.
func (c *collection[T]) rename(nameOrID string, renamed T) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	from, ok := c.nameOf(nameOrID)
	if !ok {
		return errdefs.NotFound("no %s %q", c.kind, nameOrID)
	}

	id, to := c.idAndName(renamed)
	if err := naming.Validate(to); err != nil {
		return err
	}
	if to == from {
		return nil
	}
	if _, taken := c.byName[to]; taken {
		return errdefs.Exists("%s %q already exists", c.kind, to)
	}

	// The directory moves first: a rename that fails halfway must leave the
	// definition where its files are, not where they are not. A flat
	// collection has only the file, which the write below puts in place.
	if c.layout == nested {
		if err := os.Rename(c.nestedDir(from), c.nestedDir(to)); err != nil {
			return fmt.Errorf("rename %s to %s: %w", c.nestedDir(from), c.nestedDir(to), err)
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

	delete(c.byName, from)
	c.cache(id, to, renamed)
	return nil
}

// delete removes a definition and its file. A nested collection removes the
// definition's whole directory, taking any sibling files with it.
func (c *collection[T]) delete(nameOrID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	name, ok := c.nameOf(nameOrID)
	if !ok {
		return errdefs.NotFound("no %s %q", c.kind, nameOrID)
	}

	switch c.layout {
	case nested:
		if err := os.RemoveAll(c.nestedDir(name)); err != nil {
			return fmt.Errorf("remove %s: %w", c.nestedDir(name), err)
		}
	case flat:
		if err := os.Remove(c.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", c.path(name), err)
		}
	}

	id, _ := c.idAndName(c.byName[name])
	delete(c.byName, name)
	delete(c.byID, id)
	return nil
}

// write persists a single definition atomically. It must be called with the
// lock held.
func (c *collection[T]) write(name string, definition T) error {
	data, err := yaml.Marshal(definition)
	if err != nil {
		return fmt.Errorf("marshal %q: %w", name, err)
	}

	if c.layout == nested {
		if err := os.MkdirAll(c.nestedDir(name), 0o700); err != nil {
			return fmt.Errorf("create %s: %w", c.nestedDir(name), err)
		}
	}

	return atomicfile.Write(c.path(name), data, 0o600)
}
