// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// An instance with a StandbyAfter is woken by a connection to one of its
// published TCP ports. While it is on standby its port forwarding is gone,
// and a waker listens on those ports in its place: a connection wakes the
// instance and is then relayed to its guest for as long as it lasts. The
// connections after it reach the guest through port forwarding again.

// wakeDialTimeout bounds how long a guest just woken has to accept the
// connection that woke it.
const wakeDialTimeout = 10 * time.Second

// waker is the listeners on an instance's published TCP ports while it is
// on standby.
type waker struct {
	listeners []wakeListener
}

// wakeListener listens on one of an instance's published TCP ports.
type wakeListener struct {
	net.Listener
	instance types.InstanceSpec
	port     types.PortMapping
}

// syncWaker starts an instance's waker while the instance is on standby
// with a StandbyAfter, and stops it otherwise. The caller must hold the
// instance lock, and calls it after anything that can change either.
func (m *Manager) syncWaker(ctx context.Context, instanceID string) {
	want := false
	instance, err := m.definitions.Instance(instanceID)
	if err == nil && instance.StandbyAfter > 0 && m.onStandby(instance) {
		status, err := m.Status(instance)
		want = err == nil && !status.State.IsActive()
	}

	m.wakersMu.Lock()
	_, running := m.wakers[instanceID]
	m.wakersMu.Unlock()

	switch {
	case want && !running:
		w := m.startWaker(ctx, instance)
		m.wakersMu.Lock()
		m.wakers[instanceID] = w
		m.wakersMu.Unlock()
	case !want && running:
		m.stopWaker(instanceID)
	}
}

// startWaker listens on each of an instance's published TCP ports. A port
// something else on the host holds is skipped, and logged.
func (m *Manager) startWaker(ctx context.Context, instance types.InstanceSpec) *waker {
	w := &waker{}
	for _, port := range instance.Ports {
		if port.EffectiveProtocol() != types.ProtocolTCP {
			continue
		}
		address := net.JoinHostPort(port.HostIP, strconv.Itoa(int(port.HostPort)))
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
		if err != nil {
			m.logger.WarnContext(ctx, "cannot listen to wake an instance on standby",
				"instance", instance.Name, "address", address, "error", err)
			continue
		}
		l := wakeListener{Listener: listener, instance: instance, port: port}
		w.listeners = append(w.listeners, l)
		// The listener outlives the request that started it.
		m.watchers.Go(func() { m.serveWakeListener(context.WithoutCancel(ctx), l) })
	}
	return w
}

// stopWaker stops an instance's waker, if it has one. A resume stops it
// before publishing the instance's ports again, which the host refuses
// while the waker listens on them. The caller must hold the instance lock.
func (m *Manager) stopWaker(instanceID string) {
	m.wakersMu.Lock()
	w, ok := m.wakers[instanceID]
	delete(m.wakers, instanceID)
	m.wakersMu.Unlock()

	if ok {
		w.close()
	}
}

// closeWakers stops every waker, as the daemon shuts down.
func (m *Manager) closeWakers() {
	m.wakersMu.Lock()
	defer m.wakersMu.Unlock()
	for id, w := range m.wakers {
		w.close()
		delete(m.wakers, id)
	}
}

// close stops listening.
func (w *waker) close() {
	for _, l := range w.listeners {
		_ = l.Close()
	}
}

// serveWakeListener serves each connection to l, waking l's instance for
// it, until l is closed.
func (m *Manager) serveWakeListener(ctx context.Context, l wakeListener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		// A port published on every address is not forwarded from the
		// host's loopback address, so a connection to it there does not
		// wake the instance either.
		if addr, ok := conn.LocalAddr().(*net.TCPAddr); ok && l.port.HostIP == "" && addr.IP.IsLoopback() {
			_ = conn.Close()
			continue
		}
		go m.serveWakeConnection(ctx, l, conn)
	}
}

// serveWakeConnection wakes l's instance for conn, then relays conn to its
// guest. If it cannot, conn is closed, and the instance is left on standby
// for the next connection to try.
func (m *Manager) serveWakeConnection(ctx context.Context, l wakeListener, conn net.Conn) {
	if err := m.wake(ctx, l.instance.ID, l.port.HostPort); err != nil {
		m.logger.WarnContext(ctx, "cannot wake an instance on standby",
			"instance", l.instance.Name, "port", l.port.HostPort, "error", err)
		_ = conn.Close()
		return
	}

	allocation, err := m.Allocation(l.instance)
	if err != nil {
		m.logger.WarnContext(ctx, "cannot find a woken instance's address", "instance", l.instance.Name, "error", err)
		_ = conn.Close()
		return
	}
	address := net.JoinHostPort(allocation.IP, strconv.Itoa(int(l.port.GuestPort)))

	// The host holds and retries the connection until the guest's network
	// answers, which it does as soon as the guest is resumed.
	dialCtx, cancel := context.WithTimeout(ctx, wakeDialTimeout)
	defer cancel()
	guest, err := m.dialGuest(dialCtx, address)
	if err != nil {
		m.logger.WarnContext(ctx, "the woken guest did not accept the connection that woke it",
			"instance", l.instance.Name, "address", address, "error", err)
		_ = conn.Close()
		return
	}

	relayToGuest(conn, guest)
}

// wake resumes an instance on standby for a connection to the published
// port wokenByPort, unless another connection has woken it first. The
// caller must not hold the instance lock.
func (m *Manager) wake(ctx context.Context, instanceID string, wokenByPort uint16) error {
	lock := m.lock(instanceID)
	lock.Lock()
	defer lock.Unlock()
	defer m.syncWaker(ctx, instanceID)

	instance, err := m.definitions.Instance(instanceID)
	if err != nil {
		return err
	}
	status, err := m.Status(instance)
	if err != nil {
		return err
	}

	switch {
	case status.State.IsActive():
		return nil
	case m.onStandby(instance):
		return m.resumeStandby(ctx, instance, wokenByPort)
	default:
		return errdefs.InvalidState("instance %q is %s, not on standby", instance.Name, status.State.Lowercase())
	}
}

// dialGuest connects to a guest's address over TCP.
func dialGuest(ctx context.Context, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", address)
}

// relayToGuest copies between a client's connection and the guest's in
// both directions, each side's end of writing passed on to the other, until
// both are done, then closes both.
func relayToGuest(client, guest net.Conn) {
	done := make(chan struct{}, 2)
	copyTo := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go copyTo(guest, client)
	go copyTo(client, guest)
	<-done
	<-done
	_ = client.Close()
	_ = guest.Close()
}
