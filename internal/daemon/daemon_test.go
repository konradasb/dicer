// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestStopServerEndsStreamsThatOutlastTheTimeout covers a shutdown with a
// client following a stream that never ends by itself, as 'dicer logs -f'
// does: the shutdown must not wait on it.
func TestStopServerEndsStreamsThatOutlastTheTimeout(t *testing.T) {
	opened := make(chan struct{})
	srv := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		close(opened)
		<-stream.Context().Done()
		return stream.Context().Err()
	}))

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(listener) }()

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	stream, err := conn.NewStream(t.Context(), &grpc.StreamDesc{ServerStreams: true}, "/test.Forever/Follow")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never reached the server")
	}

	start := time.Now()
	stopServer(srv, 100*time.Millisecond)
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("stopping took %s with a stream open, want about the timeout", took)
	}
}
