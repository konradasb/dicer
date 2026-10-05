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
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
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

// TestNewClientWithNoSocket checks that a socket that is not there is reported
// at once, rather than by the first call, and names what is missing.
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

// TestNewClientDefaultsToTheLocalDaemon checks that a client goes to the
// local daemon's socket unless told otherwise.
func TestNewClientDefaultsToTheLocalDaemon(t *testing.T) {
	if DefaultAddress != "unix:///run/dicer/dicer.sock" {
		t.Errorf("DefaultAddress = %q", DefaultAddress)
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

// TestNewClientOverTCP checks that a TCP address is connected to lazily, so
// a client is made without a daemon there.
func TestNewClientOverTCP(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{"plain", []Option{WithAddress("192.0.2.1:7443")}},
		{"TLS", []Option{
			WithAddress("dns:///dicer.example.com:7443"),
			WithTLS(&tls.Config{MinVersion: tls.VersionTLS13}),
		}},
		{"dial options", []Option{
			WithAddress("192.0.2.1:7443"),
			WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(tt.opts...)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			_ = c.Close()
		})
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
	blackhole := newBlackhole(t, serve(t, daemon))

	c, err := NewClient(WithAddress(blackhole.address()), WithKeepalive(10*time.Second, time.Second))
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
	blackhole.drop()

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

// GetHostInfo waits for the call to be given up.
func (d *stuckDaemon) GetHostInfo(ctx context.Context, _ *dicerdv1.GetHostInfoRequest) (*dicerdv1.GetHostInfoResponse, error) {
	close(d.called)
	<-ctx.Done()
	return nil, ctx.Err()
}

// serve serves the daemon over TCP on loopback and returns its address.
func serve(t *testing.T, daemon dicerdv1.DaemonServiceServer) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	server := grpc.NewServer()
	dicerdv1.RegisterDaemonServiceServer(server, daemon)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	return listener.Addr().String()
}

// blackhole forwards TCP connections to a target until told to drop, after
// which it keeps them open and discards whatever either end sends: what a
// network that stopped carrying packets, or a frozen host, looks like.
type blackhole struct {
	listener net.Listener
	target   string
	dropping atomic.Bool
}

// newBlackhole starts a blackhole forwarding to target, closed when the test
// ends.
func newBlackhole(t *testing.T, target string) *blackhole {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	b := &blackhole{listener: listener, target: target}
	go b.accept(t)

	return b
}

// address returns the address clients connect to.
func (b *blackhole) address() string { return b.listener.Addr().String() }

// drop stops carrying bytes, leaving the connections open.
func (b *blackhole) drop() { b.dropping.Store(true) }

// accept forwards each connection it is given until the listener closes.
func (b *blackhole) accept(t *testing.T) {
	for {
		client, err := b.listener.Accept()
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
