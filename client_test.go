// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewClientOverASocket is the whole of the local case: a client with no
// options talks to the daemon on this machine, and reaching it needs nothing
// configured and no key.
func TestNewClientOverASocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dicer.sock")
	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	c, err := NewClient(WithAddress("unix://" + path))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.Remote().SocketPath() != path {
		t.Errorf("remote = %+v, want the socket", c.Remote())
	}
	if c.Conn() == nil {
		t.Error("no connection")
	}
}

// TestNewClientRejects covers what is decided before anything is dialled.
func TestNewClientRejects(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "dicer.sock")

	for _, tc := range []struct {
		name string
		opts []Option
		want string
	}{
		{"no socket", []Option{WithAddress("unix://" + missing)}, "is dicerd running?"},
		{
			"unpinned tcp",
			[]Option{WithAddress("tcp://192.0.2.1:7443")},
			"invalid address",
		},
		{
			// Over the network the daemon asks for a certificate, and a
			// client with no key has nothing to present.
			"tcp without an identity",
			[]Option{WithAddress("tcp://192.0.2.1:7443"), WithFingerprint(testFingerprint)},
			"needs an identity",
		},
	} {
		c, err := NewClient(tc.opts...)
		if err == nil {
			_ = c.Close()
			t.Errorf("%s: NewClient succeeded", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: NewClient = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// TestNewClientOverTCP checks that a pinned remote with an identity is
// accepted. The daemon is not there; connecting is lazy, so this is as far as
// NewClient goes.
func TestNewClientOverTCP(t *testing.T) {
	pair, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}

	c, err := NewClient(
		WithAddress("tcp://192.0.2.1:7443"),
		WithFingerprint(testFingerprint),
		WithIdentity(pair),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	if got := c.Remote().HostPort(); got != "192.0.2.1:7443" {
		t.Errorf("host:port = %q", got)
	}
}

// TestLoadIdentityIsStable is why the key is kept rather than made per call:
// every daemon trusts it by fingerprint, so a new one would be a stranger to
// all of them.
func TestLoadIdentityIsStable(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	second, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("LoadIdentity again: %v", err)
	}

	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Error("the identity changed between loads")
	}

	fingerprint, err := Fingerprint(first)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") {
		t.Errorf("fingerprint = %q", fingerprint)
	}

	// The key is the client's alone: anyone who can read it can be it.
	info, err := os.Stat(filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key mode = %o, want 600", perm)
	}
}

// TestRemoteDefaultsToTheLocalDaemon pins the default down: no address means
// the socket on this machine.
func TestRemoteDefaultsToTheLocalDaemon(t *testing.T) {
	if got := LocalRemote().SocketPath(); got != DefaultSocket {
		t.Errorf("default socket = %q, want %q", got, DefaultSocket)
	}

	_, err := NewClient()
	if err == nil {
		t.Skip("a daemon is running on this machine")
	}
	if !strings.Contains(err.Error(), DefaultSocket) {
		t.Errorf("NewClient() = %v, want it to name %q", err, DefaultSocket)
	}
}

// TestOptionsDoNotLeakBetweenClients guards the usual options-slice bug: the
// dial options of one client appended into another's backing array.
func TestOptionsDoNotLeakBetweenClients(t *testing.T) {
	if err := errors.Join(); err != nil {
		t.Fatal(err)
	}

	o := options{remote: LocalRemote()}
	for _, opt := range []Option{WithAddress("tcp://192.0.2.1:7443"), WithFingerprint(testFingerprint)} {
		opt(&o)
	}

	if o.remote.Fingerprint != testFingerprint || o.remote.HostPort() != "192.0.2.1:7443" {
		t.Errorf("options = %+v, want both applied to one remote", o.remote)
	}
}
