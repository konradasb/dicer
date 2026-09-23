// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hypervisor

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// vsockHandshakeTimeout bounds the CONNECT handshake with the VMM's vsock
// proxy.
const vsockHandshakeTimeout = 5 * time.Second

// DialVsock connects to a port inside a VM through its VMM's vsock proxy.
//
// Neither supported VMM exposes the guest as a host AF_VSOCK address. Both
// Cloud Hypervisor and Firecracker instead listen on a Unix socket, and
// forward a connection to guest port P once the client sends "CONNECT P\n"
// and reads back "OK <host port>\n". One implementation serves both.
func DialVsock(ctx context.Context, socketPath string, port uint32) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial vsock socket %s: %w", socketPath, err)
	}

	deadline := time.Now().Add(vsockHandshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set handshake deadline: %w", err)
	}

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send vsock handshake: %w", err)
	}

	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read vsock handshake response (is dicer-agent running in the guest?): %w", err)
	}
	if response = strings.TrimSpace(response); !strings.HasPrefix(response, "OK ") {
		_ = conn.Close()
		return nil, fmt.Errorf("vsock handshake: %s", response)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clear handshake deadline: %w", err)
	}

	return &bufferedConn{Conn: conn, reader: reader}, nil
}

// bufferedConn reads through the reader that consumed the handshake, so any
// guest data that arrived in the same read is not lost.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
