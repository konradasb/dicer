// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAdvertisedAddresses(t *testing.T) {
	specific := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 7443}
	wildcard := &net.TCPAddr{IP: net.IPv4zero, Port: 7443}

	// Configured addresses win: behind NAT, nothing the host can see about
	// itself is how clients reach it.
	got, err := advertisedAddresses([]string{"dicer.example.com:443"}, wildcard)
	if err != nil || !slices.Equal(got, []string{"dicer.example.com:443"}) {
		t.Errorf("configured: %v, %v", got, err)
	}

	// A listener bound to one address is reached at it.
	got, err = advertisedAddresses(nil, specific)
	if err != nil || !slices.Equal(got, []string{"192.0.2.1:7443"}) {
		t.Errorf("specific: %v, %v", got, err)
	}

	// One bound to all of them is reached at the host's own, on its port,
	// and never at loopback, which means nothing to another machine.
	got, err = advertisedAddresses(nil, wildcard)
	if err != nil {
		t.Skipf("this host has no routable address: %v", err)
	}
	for _, addr := range got {
		host, port, _ := net.SplitHostPort(addr)
		if port != "7443" || net.ParseIP(host).IsLoopback() || strings.HasPrefix(host, "0.") {
			t.Errorf("wildcard advertised %q", addr)
		}
	}
}

func TestListenSocketSetsItsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dicer.sock")

	l, err := listenSocket(t.Context(), SocketConfig{Path: path, Mode: 0o660})
	if err != nil {
		t.Fatalf("listenSocket: %v", err)
	}
	defer func() { _ = l.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o660 {
		t.Errorf("mode = %o, want 660", got)
	}
}

func TestListenSocketReplacesAStaleOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dicer.sock")

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	// Closed without unlinking, as a daemon that died would leave it.
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()

	l, err := listenSocket(t.Context(), SocketConfig{Path: path})
	if err != nil {
		t.Fatalf("listenSocket over a stale socket: %v", err)
	}
	_ = l.Close()
}

func TestListenSocketRefusesALiveOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dicer.sock")

	live, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close() }()

	if l, err := listenSocket(t.Context(), SocketConfig{Path: path}); err == nil {
		_ = l.Close()
		t.Fatal("listenSocket took over a socket another daemon is serving on")
	}
}
