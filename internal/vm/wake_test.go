// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
)

// wakeHarness is a harness whose instance publishes a port on the loopback
// address and goes on standby by itself, and whose guest is an echo server.
type wakeHarness struct {
	*harness
	port int

	mu     sync.Mutex
	dialed []string
}

func newWakeHarness(t *testing.T, standbyAfter time.Duration) *wakeHarness {
	t.Helper()

	h := &wakeHarness{harness: newHarness(t), port: freePort(t)}
	h.instance.StandbyAfter = standbyAfter
	h.instance.Ports = []types.PortMapping{{HostIP: "127.0.0.1", HostPort: uint16(h.port), GuestPort: 80}}
	h.definitions.instances[h.instance.Name] = h.instance

	guest := echoServer(t)
	h.manager.dialGuest = func(ctx context.Context, address string) (net.Conn, error) {
		h.mu.Lock()
		h.dialed = append(h.dialed, address)
		h.mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, "tcp", guest)
	}

	h.start(t)
	if err := h.manager.Standby(t.Context(), h.instance); err != nil {
		t.Fatal(err)
	}
	return h
}

// TestConnectionWakesInstanceOnStandby checks that a connection to a port an
// instance on standby publishes resumes it, and reaches its guest.
func TestConnectionWakesInstanceOnStandby(t *testing.T) {
	h := newWakeHarness(t, 15*time.Minute)

	conn := h.dial(t)
	if got := roundTrip(t, conn, "ping"); got != "ping" {
		t.Errorf("the guest answered %q, want the echo of %q", got, "ping")
	}

	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("state = %s, want %s", status.State, types.InstanceStateRunning)
	}
	allocation, err := h.manager.Allocation(h.instance)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	dialed := h.dialed
	h.mu.Unlock()
	if want := net.JoinHostPort(allocation.IP, "80"); len(dialed) == 0 || dialed[0] != want {
		t.Errorf("forwarded to %v, want the guest's %s", dialed, want)
	}
	if e, _ := h.events.last(events.ActionStarted); e.Attributes["woken_by_port"] != strconv.Itoa(h.port) {
		t.Errorf("start event = %+v, want it to say what woke the instance", e)
	}
	if h.listening() {
		t.Error("the daemon still listens on the port of a running instance")
	}
}

// TestManualStandbyIsNotWoken checks that an instance without standby_after,
// put on standby by hand, is not woken by a connection.
func TestManualStandbyIsNotWoken(t *testing.T) {
	h := newWakeHarness(t, 0)

	if h.listening() {
		t.Error("the daemon listens to wake an instance that has no standby_after")
	}
}

// TestFailedWakeLeavesInstanceToBeWokenAgain checks that a connection that
// cannot wake its instance is closed, and that the next can.
func TestFailedWakeLeavesInstanceToBeWokenAgain(t *testing.T) {
	h := newWakeHarness(t, 15*time.Minute)
	// A wake reads the starter under the instance lock, so the test
	// changes it under the lock too.
	lock := h.manager.lock(h.instance.ID)
	lock.Lock()
	h.starter.restoreErr = errors.New("hypervisor refused")
	lock.Unlock()

	conn := h.dial(t)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection that could not wake its instance was answered")
	}
	if !h.listening() {
		t.Fatal("the daemon stopped listening after a failed wake")
	}

	lock.Lock()
	h.starter.restoreErr = nil
	lock.Unlock()

	if got := roundTrip(t, h.dial(t), "again"); got != "again" {
		t.Errorf("the guest answered %q, want %q", got, "again")
	}
}

// TestStopEndsListening checks that stopping an instance on standby, which
// discards what it froze, stops the daemon listening to wake it.
func TestStopEndsListening(t *testing.T) {
	h := newWakeHarness(t, 15*time.Minute)
	if !h.listening() {
		t.Fatal("the daemon does not listen to wake an instance on standby")
	}

	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatal(err)
	}
	if h.listening() {
		t.Error("the daemon still listens to wake a stopped instance")
	}
}

// dial connects to the instance's published port.
func (h *wakeHarness) dial(t *testing.T) net.Conn {
	t.Helper()

	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.port)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// listening reports whether the daemon listens to wake the instance.
func (h *wakeHarness) listening() bool {
	h.manager.wakersMu.Lock()
	defer h.manager.wakersMu.Unlock()
	_, ok := h.manager.wakers[h.instance.ID]
	return ok
}

// roundTrip writes message to conn and returns what comes back.
func roundTrip(t *testing.T, conn net.Conn, message string) string {
	t.Helper()

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return string(got)
}

// echoServer serves, on a loopback port, what each connection writes back
// to it, and returns its address.
func echoServer(t *testing.T) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return l.Addr().String()
}

// freePort returns a loopback port nothing listens on.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listening on %v, not a TCP address", l.Addr())
	}
	return addr.Port
}

// TestStartResumesInstanceTheDaemonListensFor checks that starting an
// instance on standby that wakes on a connection resumes it: the daemon
// stops listening on its ports for them to be published again.
func TestStartResumesInstanceTheDaemonListensFor(t *testing.T) {
	h := newWakeHarness(t, 15*time.Minute)

	if err := h.manager.Start(t.Context(), h.instance); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("state = %s, want %s", status.State, types.InstanceStateRunning)
	}
	if h.listening() {
		t.Error("the daemon still listens on the port of a running instance")
	}
}
