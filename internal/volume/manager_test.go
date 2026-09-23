// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package volume

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(Config{DataDir: t.TempDir()})
	m.createDisk = func(_ context.Context, _ string, _ int64) error { return nil }
	return m
}

// --- Create ---

func TestManager_Create(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	vol, err := m.Create(ctx, "my-vol", 1024)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if vol.ID == "" {
		t.Error("ID should be non-empty")
	}
	if vol.Name != "my-vol" {
		t.Errorf("Name = %q, want %q", vol.Name, "my-vol")
	}
	if vol.SizeBytes != 1024 {
		t.Errorf("SizeBytes = %d, want 1024", vol.SizeBytes)
	}
	if vol.Path == "" {
		t.Error("Path should be non-empty")
	}
	if vol.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestManager_CreateInvalidSize(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	_, err := m.Create(ctx, "vol", 0)
	if err == nil {
		t.Fatal("Create() should return error for zero size")
	}
}

func TestManager_CreateError(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	provErr := errors.New("mkfs failed")
	m.createDisk = func(_ context.Context, _ string, _ int64) error { return provErr }

	_, err := m.Create(ctx, "vol", 512)
	if err == nil {
		t.Fatal("Create() should propagate the disk creation error")
	}
	if !errors.Is(err, provErr) {
		t.Errorf("error = %v, want to wrap %v", err, provErr)
	}
}

// --- Delete ---

func TestManager_Delete(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	vol, err := m.Create(ctx, "to-delete", 512)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := m.Delete(vol.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := os.Stat(m.volumeDir(vol.ID)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("volume directory should be removed after Delete")
	}
}

func TestManager_Delete_Idempotent(t *testing.T) {
	m := newTestManager(t)

	if err := m.Delete("ghost"); err != nil {
		t.Errorf("Delete() nonexistent error = %v, want nil", err)
	}
}
