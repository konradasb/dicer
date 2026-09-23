// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hypervisor

import (
	"bufio"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

// fakeVsockProxy accepts one connection and answers the CONNECT handshake
// with reply, then writes greeting to the client.
func fakeVsockProxy(t *testing.T, reply, greeting string) (string, <-chan string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "vsock.sock")
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	requests := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		line, _ := bufio.NewReader(conn).ReadString('\n')
		requests <- line
		_, _ = io.WriteString(conn, reply+greeting)
	}()

	return path, requests
}

func TestDialVsock(t *testing.T) {
	// The greeting arrives in the same write as the handshake reply, so it
	// is only seen if the dialer's buffered bytes are handed on.
	path, requests := fakeVsockProxy(t, "OK 1073741824\n", "hello")

	conn, err := DialVsock(t.Context(), path, 2222)
	if err != nil {
		t.Fatalf("DialVsock: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if got := <-requests; got != "CONNECT 2222\n" {
		t.Errorf("handshake = %q, want CONNECT 2222", got)
	}

	got, _ := io.ReadAll(conn)
	if string(got) != "hello" {
		t.Errorf("read %q after the handshake, want hello", got)
	}
}

func TestDialVsockRejected(t *testing.T) {
	path, _ := fakeVsockProxy(t, "FAILURE\n", "")

	_, err := DialVsock(t.Context(), path, 2222)
	if err == nil || !strings.Contains(err.Error(), "FAILURE") {
		t.Errorf("DialVsock = %v, want the proxy's refusal", err)
	}
}

func TestTypeValid(t *testing.T) {
	for _, typ := range types.HypervisorTypes() {
		if !typ.Valid() {
			t.Errorf("%s is listed but not valid", typ)
		}
	}
	if types.HypervisorType("qemu").Valid() {
		t.Error("an unsupported type is valid")
	}
}
