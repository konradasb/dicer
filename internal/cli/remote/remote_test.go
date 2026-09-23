// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package remote

import (
	"errors"
	"slices"
	"testing"

	"github.com/dicer-sh/dicer"
)

const testFingerprint = "sha256:" + "ab00000000000000000000000000000000000000000000000000000000000000"

func TestLoadWithoutAFileHasOnlyLocal(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.CurrentName(); got != Local {
		t.Errorf("current = %q, want %q", got, Local)
	}
	if got := cfg.Names(); !slices.Equal(got, []string{Local}) {
		t.Errorf("names = %v, want only %q", got, Local)
	}

	r, err := cfg.Get(Local)
	if err != nil {
		t.Fatalf("Get(local): %v", err)
	}
	if r.SocketPath() != dicer.DefaultSocket {
		t.Errorf("local socket = %q, want %q", r.SocketPath(), dicer.DefaultSocket)
	}
}

func TestCreateUseSaveLoad(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := Load(dir)

	if err := cfg.Create("prod", dicer.TCPRemote("192.0.2.1:7443", testFingerprint)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := cfg.Use("prod"); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.CurrentName() != "prod" {
		t.Errorf("current = %q, want prod", loaded.CurrentName())
	}
	r, err := loaded.Get("prod")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.HostPort() != "192.0.2.1:7443" || r.Fingerprint != testFingerprint {
		t.Errorf("remote = %+v, want the one created", r)
	}
}

func TestDeletingCurrentFallsBackToLocal(t *testing.T) {
	cfg, _ := Load(t.TempDir())
	_ = cfg.Create("prod", dicer.TCPRemote("192.0.2.1:7443", testFingerprint))
	_ = cfg.Use("prod")

	if err := cfg.Delete("prod"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if cfg.CurrentName() != Local {
		t.Errorf("current = %q after deleting it, want %q", cfg.CurrentName(), Local)
	}
}

func TestCreateRejections(t *testing.T) {
	cfg, _ := Load(t.TempDir())
	_ = cfg.Create("prod", dicer.TCPRemote("192.0.2.1:7443", testFingerprint))

	for _, tc := range []struct {
		name   string
		remote dicer.Remote
		want   error
	}{
		{Local, dicer.SocketRemote("/tmp/dicer.sock"), dicer.ErrInvalidArgument},
		{"prod", dicer.SocketRemote("/tmp/dicer.sock"), dicer.ErrExists},
		{"../x", dicer.SocketRemote("/tmp/dicer.sock"), dicer.ErrInvalidArgument},
		{"relative", dicer.SocketRemote("dicer.sock"), dicer.ErrInvalidArgument},
		{"unpinned", dicer.Remote{Address: "tcp://192.0.2.1:7443"}, nil},
		{"no-scheme", dicer.Remote{Address: "192.0.2.1:7443"}, dicer.ErrInvalidArgument},
	} {
		err := cfg.Create(tc.name, tc.remote)
		if err == nil {
			t.Errorf("Create(%q, %+v) succeeded", tc.name, tc.remote)
			continue
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("Create(%q) = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestLocalCannotBeDeleted(t *testing.T) {
	cfg, _ := Load(t.TempDir())

	if err := cfg.Delete(Local); !errors.Is(err, dicer.ErrInvalidArgument) {
		t.Errorf("Delete(local) = %v, want ErrInvalidArgument", err)
	}
}
