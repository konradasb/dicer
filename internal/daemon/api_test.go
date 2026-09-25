// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

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

// The socket's group may be given by ID or by name. Tests run as whoever runs
// them, so the group is their own: the only one they can give a file.
func TestListenSocketSetsItsGroup(t *testing.T) {
	gid := os.Getgid()
	g, err := user.LookupGroupId(strconv.Itoa(gid))
	if err != nil {
		t.Skipf("the test's own group has no name: %v", err)
	}

	for _, group := range []string{strconv.Itoa(gid), g.Name} {
		t.Run(group, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dicer.sock")

			l, err := listenSocket(t.Context(), SocketConfig{Path: path, Group: group})
			if err != nil {
				t.Fatalf("listenSocket: %v", err)
			}
			defer func() { _ = l.Close() }()

			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := int(info.Sys().(*syscall.Stat_t).Gid); got != gid {
				t.Errorf("group = %d, want %d", got, gid)
			}
		})
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
