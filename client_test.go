// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"crypto/tls"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
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
