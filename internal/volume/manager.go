// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package volume provisions the persistent disks instances mount: sparse
// files with a filesystem, independent of any instance.
package volume

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nrednav/cuid2"

	"github.com/konradasb/dicer/internal/types"
)

// Config configures a Manager.
type Config struct {
	// DataDir is the directory volume disks are kept under.
	DataDir string

	Logger *slog.Logger
}

// Manager owns the disk files that back volumes. It records no metadata: the
// types.Volume definition lives in the definition store, and its disk is found by
// ID.
type Manager struct {
	dataDir string
	logger  *slog.Logger

	// createDisk is the disk creation itself, replaced in tests.
	createDisk func(ctx context.Context, path string, sizeBytes int64) error
}

// NewManager creates a Manager.
func NewManager(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Manager{
		dataDir:    cfg.DataDir,
		logger:     cfg.Logger.With("component", "volume"),
		createDisk: createVolumeDisk,
	}
}

// Path returns the path of a volume's disk file.
func (m *Manager) Path(id string) string {
	return filepath.Join(m.volumeDir(id), "disk.raw")
}

func (m *Manager) volumeDir(id string) string {
	return filepath.Join(m.dataDir, "volumes", id)
}

// Create makes a new volume: a sparse, ext4-formatted disk file under a
// fresh ID. Recording the returned types.Volume is the caller's job.
func (m *Manager) Create(ctx context.Context, name string, sizeBytes int64) (*types.Volume, error) {
	if sizeBytes <= 0 {
		return nil, errors.New("size must be greater than zero")
	}

	id := cuid2.Generate()

	if err := m.createDisk(ctx, m.Path(id), sizeBytes); err != nil {
		_ = os.RemoveAll(m.volumeDir(id))
		return nil, fmt.Errorf("create disk: %w", err)
	}

	now := time.Now()
	vol := types.Volume{
		ID:        id,
		Name:      name,
		Path:      m.Path(id),
		SizeBytes: sizeBytes,
		CreatedAt: now,
		UpdatedAt: now,
	}

	m.logger.InfoContext(ctx, "volume created", "id", id, "name", name, "size_bytes", sizeBytes)

	return &vol, nil
}

// Delete removes a volume's disk.
func (m *Manager) Delete(id string) error {
	return os.RemoveAll(m.volumeDir(id))
}

// createVolumeDisk creates a sparse ext4-formatted disk file at path.
func createVolumeDisk(ctx context.Context, path string, sizeBytes int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create disk file: %w", err)
	}
	if err := f.Truncate(sizeBytes); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("allocate disk: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}

	out, err := exec.CommandContext(ctx, "mkfs.ext4", "-F", path).CombinedOutput()
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("format disk: %w: %s", err, out)
	}

	return nil
}
