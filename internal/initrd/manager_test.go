// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/registry"
)

// fakePuller exports an empty root filesystem.
type fakePuller struct{}

func (fakePuller) Resolve(context.Context, *reference.Ref) (string, error) {
	return "sha256:test", nil
}

func (fakePuller) PullAndExport(
	_ context.Context, _, digest, exportDir string, _ registry.EventFunc,
) (*registry.PullResult, error) {
	if err := os.MkdirAll(filepath.Join(exportDir, "etc"), 0o755); err != nil {
		return nil, err
	}
	return &registry.PullResult{Digest: digest}, nil
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(Config{Puller: fakePuller{}, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m
}

func TestBuild_ReplacesInitrdAtFixedPath(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	const arch = "x86_64"

	if m.current(arch, "one") {
		t.Fatal("current before any build")
	}

	if err := m.build(ctx, arch, "one", []byte("init-1"), []byte("agent-1")); err != nil {
		t.Fatalf("first build: %v", err)
	}
	if !m.current(arch, "one") {
		t.Fatal("not current after first build")
	}

	// A process that opened the first build keeps reading it after the
	// second replaces the path.
	f, err := os.Open(m.initrdPath(arch))
	if err != nil {
		t.Fatalf("open first build: %v", err)
	}
	defer func() { _ = f.Close() }()
	first, err := f.Stat()
	if err != nil {
		t.Fatalf("stat first build: %v", err)
	}

	if err := m.build(ctx, arch, "two", []byte("init-2"), []byte("agent-2")); err != nil {
		t.Fatalf("second build: %v", err)
	}
	if m.current(arch, "one") || !m.current(arch, "two") {
		t.Fatal("hash not updated by second build")
	}

	second, err := os.Stat(m.initrdPath(arch))
	if err != nil {
		t.Fatalf("stat second build: %v", err)
	}
	if os.SameFile(first, second) {
		t.Fatal("second build wrote into the first build's file instead of replacing it")
	}
}
