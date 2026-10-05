// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
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
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatalf("stat of %s is %T, want *syscall.Stat_t", path, info.Sys())
			}
			if got := int(st.Gid); got != gid {
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

// TestKeepaliveLetsClientsPing checks that the daemon's default keepalive
// lets a client ping as often as gRPC allows, between calls too, without
// being disconnected, as gRPC's own defaults would after a few pings.
func TestKeepaliveLetsClientsPing(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for several keepalive pings, which gRPC sends 10s apart at the soonest")
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingListener{Listener: listener}

	s := grpc.NewServer(keepaliveOptions(defaultConfig().API.Keepalive)...)
	dicerdv1.RegisterDaemonServiceServer(s, hostInfoServer{})
	go func() { _ = s.Serve(counted) }()
	t.Cleanup(s.Stop)

	c, err := dicer.NewClient(dicer.WithAddress(listener.Addr().String()), dicer.WithKeepalive(10*time.Second, 5*time.Second))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	call := func() {
		t.Helper()
		if _, err := c.GetHostInfo(t.Context(), &dicerdv1.GetHostInfoRequest{}); err != nil {
			t.Fatalf("GetHostInfo: %v", err)
		}
	}

	call()
	// Long enough for three pings, which gRPC's defaults disconnect for.
	time.Sleep(35 * time.Second)
	call()

	// A client that was disconnected reconnects without its caller seeing,
	// so it shows only in the connections the daemon accepted.
	if n := counted.accepted.Load(); n != 1 {
		t.Errorf("the daemon accepted %d connections, want the client kept on 1", n)
	}
}

// hostInfoServer answers GetHostInfo at once.
type hostInfoServer struct {
	dicerdv1.UnimplementedDaemonServiceServer
}

func (hostInfoServer) GetHostInfo(context.Context, *dicerdv1.GetHostInfoRequest) (*dicerdv1.GetHostInfoResponse, error) {
	return &dicerdv1.GetHostInfoResponse{}, nil
}

// countingListener counts the connections it accepts.
type countingListener struct {
	net.Listener
	accepted atomic.Int32
}

// Accept accepts a connection and counts it.
func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}
