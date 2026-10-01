// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/konradasb/dicer/internal/types"
)

// firewalldZone is the firewalld zone a network's bridge is bound to: one
// the packages install, from build/package/firewalld-zone.xml.
const firewalldZone = "dicer"

// firewalldStartTimeout bounds how long a firewalld that has just taken its
// name on the bus may take to finish starting.
const firewalldStartTimeout = 30 * time.Second

// firewalld's D-Bus names.
const (
	firewalldName      = "org.fedoraproject.FirewallD1"
	firewalldPath      = "/org/fedoraproject/FirewallD1"
	firewalldZoneIface = firewalldName + ".zone"
)

// errNoFirewalldZone is firewalld running without firewalldZone, as on a
// host where Dicer was not installed from a package.
var errNoFirewalldZone = fmt.Errorf(
	"firewalld has no %q zone: install it from Dicer's package, or copy "+
		"build/package/firewalld-zone.xml to /etc/firewalld/zones/%s.xml and run firewall-cmd --reload",
	firewalldZone, firewalldZone)

// firewalld manages bridges' places in firewalld, whose own rules Dicer's
// iptables rules cannot override. Its methods do nothing while firewalld is
// not running.
type firewalld struct {
	conn *dbus.Conn
}

// connectFirewalld connects to firewalld over the system bus. It fails only
// if there is no system bus, not if firewalld is not running.
func connectFirewalld() (*firewalld, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect to the system bus: %w", err)
	}
	return &firewalld{conn: conn}, nil
}

// WatchFirewalld sets every network's bridge up again each time firewalld
// reloads or starts, either of which drops the bridges' places in its zones
// and flushes Dicer's iptables rules. It returns when ctx ends, and at once
// where there is no system bus.
func (h *Host) WatchFirewalld(ctx context.Context) {
	if h.firewalld == nil {
		return
	}
	err := h.firewalld.watchReloads(ctx, func() {
		h.mu.Lock()
		networks := make([]types.Network, 0, len(h.networks))
		for _, nw := range h.networks {
			networks = append(networks, nw)
		}
		h.mu.Unlock()

		h.logger.InfoContext(ctx, "firewalld reloaded, setting bridges up again", "bridges", len(networks))
		for _, nw := range networks {
			if err := h.SetupBridge(ctx, &nw); err != nil {
				h.logger.WarnContext(ctx, "failed to set bridge up again after firewalld reloaded",
					"network", nw.Name, "bridge", nw.Bridge, "error", err)
			}
		}
	})
	if err != nil {
		h.logger.WarnContext(ctx, "cannot watch firewalld: bridges are not set up again when it reloads",
			"error", err)
	}
}

func (f *firewalld) close() { _ = f.conn.Close() }

// isRunning reports whether firewalld is on the bus.
func (f *firewalld) isRunning(ctx context.Context) bool {
	var running bool
	err := f.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, firewalldName).
		Store(&running)
	return err == nil && running
}

// call calls one of firewalld's methods, storing its result in out unless
// out is nil.
func (f *firewalld) call(ctx context.Context, method string, out any, args ...any) error {
	call := f.conn.Object(firewalldName, firewalldPath).CallWithContext(ctx, method, 0, args...)
	if out == nil {
		return call.Err
	}
	return call.Store(out)
}

// bind puts a bridge in firewalldZone, taking it out of any other. It
// returns errNoFirewalldZone if firewalld has no such zone. The binding is
// firewalld's runtime configuration, which a reload drops: watchReloads
// says when to bind again.
func (f *firewalld) bind(ctx context.Context, bridge string) error {
	if !f.isRunning(ctx) {
		return nil
	}

	var zones []string
	if err := f.call(ctx, firewalldZoneIface+".getZones", &zones); err != nil {
		return fmt.Errorf("list firewalld zones: %w", err)
	}
	if !slices.Contains(zones, firewalldZone) {
		return errNoFirewalldZone
	}

	var zone string
	if err := f.call(ctx, firewalldZoneIface+".getZoneOfInterface", &zone, bridge); err != nil {
		return fmt.Errorf("find the firewalld zone of %s: %w", bridge, err)
	}
	if zone == firewalldZone {
		return nil
	}
	if err := f.call(ctx, firewalldZoneIface+".changeZoneOfInterface", nil, firewalldZone, bridge); err != nil {
		return fmt.Errorf("bind %s to firewalld zone %q: %w", bridge, firewalldZone, err)
	}
	return nil
}

// unbind takes a bridge out of firewalldZone, if it is in it.
func (f *firewalld) unbind(ctx context.Context, bridge string) error {
	if !f.isRunning(ctx) {
		return nil
	}

	var zone string
	if err := f.call(ctx, firewalldZoneIface+".getZoneOfInterface", &zone, bridge); err != nil {
		return fmt.Errorf("find the firewalld zone of %s: %w", bridge, err)
	}
	if zone != firewalldZone {
		return nil
	}
	if err := f.call(ctx, firewalldZoneIface+".removeInterface", nil, firewalldZone, bridge); err != nil {
		return fmt.Errorf("unbind %s from firewalld zone %q: %w", bridge, firewalldZone, err)
	}
	return nil
}

// watchReloads calls reloaded each time firewalld reloads or starts, either
// of which drops its runtime configuration, until ctx ends.
func (f *firewalld) watchReloads(ctx context.Context, reloaded func()) error {
	matches := [][]dbus.MatchOption{
		{dbus.WithMatchInterface(firewalldName), dbus.WithMatchMember("Reloaded")},
		{
			dbus.WithMatchInterface("org.freedesktop.DBus"),
			dbus.WithMatchMember("NameOwnerChanged"),
			dbus.WithMatchArg(0, firewalldName),
		},
	}
	for _, match := range matches {
		if err := f.conn.AddMatchSignalContext(ctx, match...); err != nil {
			return fmt.Errorf("watch firewalld: %w", err)
		}
	}

	signals := make(chan *dbus.Signal, 8)
	f.conn.Signal(signals)
	defer f.conn.RemoveSignal(signals)

	for {
		select {
		case <-ctx.Done():
			return nil
		case s, ok := <-signals:
			if !ok {
				return errors.New("watch firewalld: the system bus connection closed")
			}
			switch {
			case s.Name == firewalldName+".Reloaded":
				reloaded()
			case isFirewalldStart(s):
				// It takes its name before it has loaded its rules, which
				// flushes iptables: setting up before it is done would be
				// undone.
				if err := f.waitUntilRunning(ctx); err != nil {
					return err
				}
				reloaded()
			}
		}
	}
}

// waitUntilRunning waits until firewalld reports it has finished starting,
// for at most firewalldStartTimeout.
func (f *firewalld) waitUntilRunning(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, firewalldStartTimeout)
	defer cancel()

	for {
		var state dbus.Variant
		err := f.call(ctx, "org.freedesktop.DBus.Properties.Get", &state, firewalldName, "state")
		if err == nil && state.Value() == "RUNNING" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for firewalld to start: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// isFirewalldStart reports whether a signal is firewalld taking its name on
// the bus, as it does when it starts.
func isFirewalldStart(s *dbus.Signal) bool {
	if s.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(s.Body) != 3 {
		return false
	}
	name, _ := s.Body[0].(string)
	newOwner, _ := s.Body[2].(string)
	return name == firewalldName && newOwner != ""
}
