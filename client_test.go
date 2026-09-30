// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// TestNewClientOverASocket checks the local case end to end: a client with
// the socket's address reaches the server behind it.
func TestNewClientOverASocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dicer.sock")
	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, health.NewServer())
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	c, err := NewClient(WithAddress("unix://" + path))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := healthpb.NewHealthClient(c.conn).Check(t.Context(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Errorf("a call over the socket failed: %v", err)
	}
}

// A socket that is not there is reported at once, rather than by the first
// call, and names what is missing.
func TestNewClientWithNoSocket(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "dicer.sock")

	c, err := NewClient(WithAddress("unix://" + missing))
	if err == nil {
		_ = c.Close()
		t.Fatal("NewClient succeeded")
	}
	if !strings.Contains(err.Error(), "is dicerd running?") || !strings.Contains(err.Error(), missing) {
		t.Errorf("NewClient = %v, want it to name the missing socket", err)
	}
}

// By default a client goes to the local daemon.
func TestNewClientDefaultsToTheLocalDaemon(t *testing.T) {
	o := options{address: DefaultAddress}
	if o.address != "unix:///run/dicer/dicer.sock" {
		t.Errorf("default address = %q", o.address)
	}

	c, err := NewClient()
	if err == nil {
		_ = c.Close()
		t.Skip("a daemon is running on this machine")
	}
	if !strings.Contains(err.Error(), "/run/dicer/dicer.sock") {
		t.Errorf("NewClient() = %v, want it to name the default socket", err)
	}
}

// A TCP address is connected to lazily, so a client is made without a
// daemon there.
func TestNewClientOverTCP(t *testing.T) {
	for _, opts := range [][]Option{
		{WithAddress("192.0.2.1:7443")},
		{WithAddress("dns:///dicer.example.com:7443"), WithTLS(&tls.Config{MinVersion: tls.VersionTLS13})},
		{WithAddress("192.0.2.1:7443"), WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()))},
	} {
		c, err := NewClient(opts...)
		if err != nil {
			t.Errorf("NewClient: %v", err)
			continue
		}
		_ = c.Close()
	}
}

// TestKeepaliveGivesUpOnADaemonThatStopsAnswering checks that a call in
// flight fails once the daemon stops answering without closing the
// connection, rather than waiting for as long as its context lets it.
func TestKeepaliveGivesUpOnADaemonThatStopsAnswering(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a keepalive ping, which gRPC sends 10s apart at the soonest")
	}

	daemon := &stuckDaemon{called: make(chan struct{})}
	network := newBlackhole(t, serve(t, daemon))

	c, err := NewClient(WithAddress(network.addr()), WithKeepalive(10*time.Second, time.Second))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := c.GetHostInfo(ctx, &dicerdv1.GetHostInfoRequest{})
		done <- err
	}()

	<-daemon.called
	network.drop()

	select {
	case err := <-done:
		if status.Code(err) != codes.Unavailable {
			t.Errorf("call = %v, want Unavailable", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the call still waits 30s after the daemon stopped answering")
	}
}

// stuckDaemon answers GetHostInfo never, as a daemon on a frozen host.
type stuckDaemon struct {
	dicerdv1.UnimplementedDaemonServiceServer

	// called is closed once the call has arrived.
	called chan struct{}
}

func (d *stuckDaemon) GetHostInfo(ctx context.Context, _ *dicerdv1.GetHostInfoRequest) (*dicerdv1.GetHostInfoResponse, error) {
	close(d.called)
	<-ctx.Done()
	return nil, ctx.Err()
}

// serve serves the daemon over TCP on loopback and returns its address.
func serve(t *testing.T, daemon dicerdv1.DaemonServiceServer) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	s := grpc.NewServer()
	dicerdv1.RegisterDaemonServiceServer(s, daemon)
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(s.Stop)

	return ln.Addr().String()
}

// blackhole forwards TCP connections to a target until told to drop, after
// which it keeps them open and discards whatever either end sends: what a
// network that stopped carrying packets, or a frozen host, looks like.
type blackhole struct {
	ln       net.Listener
	target   string
	dropping atomic.Bool
}

func newBlackhole(t *testing.T, target string) *blackhole {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	b := &blackhole{ln: ln, target: target}
	go b.accept(t)

	return b
}

func (b *blackhole) addr() string { return b.ln.Addr().String() }

// drop stops carrying bytes, leaving the connections open.
func (b *blackhole) drop() { b.dropping.Store(true) }

func (b *blackhole) accept(t *testing.T) {
	for {
		client, err := b.ln.Accept()
		if err != nil {
			return
		}
		server, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", b.target)
		if err != nil {
			_ = client.Close()
			continue
		}
		t.Cleanup(func() {
			_ = client.Close()
			_ = server.Close()
		})

		go b.carry(server, client)
		go b.carry(client, server)
	}
}

// carry copies from src to dst until src closes, discarding once dropping.
func (b *blackhole) carry(dst io.Writer, src io.Reader) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !b.dropping.Load() {
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
