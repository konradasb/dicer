// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/vishvananda/netlink"

	"github.com/dicer-sh/dicer"
)

// SetupBridge creates the bridge, iptables rules, and root qdisc for a
// network on this host if they don't already exist. Called lazily, when an
// instance on the network starts.
//
// Every step is idempotent and each is checked every time, not only when the
// bridge is new: a setup that failed part way leaves the bridge behind, and
// a firewall reload can flush the rules from under one that is up.
func (h *Host) SetupBridge(ctx context.Context, nw *dicer.Network) error {
	_, ipNet, err := net.ParseCIDR(nw.Subnet)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSubnet, err)
	}

	if err := h.ensureBridge(ctx, nw, ipNet); err != nil {
		return fmt.Errorf("create bridge: %w", err)
	}

	if err := h.setupIPTables(ctx, nw.Bridge, nw.Subnet, nw.Gateway); err != nil {
		return fmt.Errorf("setup iptables: %w", err)
	}

	if err := ensureBridgeQdisc(nw.Bridge, h.config.UplinkCapacityBps); err != nil {
		return fmt.Errorf("setup bridge qdisc: %w", err)
	}

	return nil
}

// TeardownBridge removes the bridge, iptables rules, and qdisc for a network.
// Best-effort: logs failures but does not return an error.
func (h *Host) TeardownBridge(ctx context.Context, nw *dicer.Network) {
	if err := deleteBridge(nw.Bridge); err != nil {
		h.logger.WarnContext(ctx, "failed to delete bridge",
			"bridge", nw.Bridge, "error", err)
	}
	h.teardownIPTables(ctx, nw.Bridge)

	h.logger.InfoContext(ctx, "bridge torn down", "network_id", nw.ID, "bridge", nw.Bridge)
}

// ensureBridge creates the network's bridge if it does not exist, and makes
// sure it is up and holds the gateway address.
func (h *Host) ensureBridge(ctx context.Context, nw *dicer.Network, ipNet *net.IPNet) error {
	br, err := netlink.LinkByName(nw.Bridge)
	switch {
	case isLinkNotFound(err):
		h.logger.InfoContext(ctx, "setting up bridge", "network_id", nw.ID, "bridge", nw.Bridge)
		br = &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: nw.Bridge}}
		if err := netlink.LinkAdd(br); err != nil {
			return fmt.Errorf("create bridge: %w", err)
		}
	case err != nil:
		return fmt.Errorf("look up bridge %s: %w", nw.Bridge, err)
	}

	if err := netlink.LinkSetUp(br); err != nil {
		return fmt.Errorf("set bridge up: %w", err)
	}

	addr := &netlink.Addr{
		IPNet: &net.IPNet{IP: net.ParseIP(nw.Gateway), Mask: ipNet.Mask},
	}
	if err := netlink.AddrReplace(br, addr); err != nil {
		return fmt.Errorf("add gateway to bridge: %w", err)
	}

	return nil
}

func deleteBridge(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		if isLinkNotFound(err) {
			return nil // already gone; deleting is idempotent
		}
		return fmt.Errorf("look up bridge %s: %w", name, err)
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("delete bridge %s: %w", name, err)
	}

	return nil
}

// isLinkNotFound reports whether err means the interface does not exist, as
// opposed to netlink being unreachable or refusing the request. Treating
// every lookup failure as "already gone" would silently skip teardown.
func isLinkNotFound(err error) bool {
	var notFound netlink.LinkNotFoundError
	return errors.As(err, &notFound)
}
