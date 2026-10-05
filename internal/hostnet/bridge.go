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

	"github.com/konradasb/dicer/internal/types"
)

// SetupBridge ensures the bridge, iptables rules and gateway access for a
// network exist. Every step is idempotent and checked on each call. The
// network is remembered, to set up again when firewalld reloads, until
// TeardownBridge.
func (h *Host) SetupBridge(ctx context.Context, nw *types.Network) error {
	_, ipNet, err := net.ParseCIDR(nw.Subnet)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSubnet, err)
	}

	if err := h.ensureBridge(ctx, nw, ipNet); err != nil {
		return err
	}

	h.mu.Lock()
	h.networks[nw.Bridge] = *nw
	h.mu.Unlock()

	if err := h.setupIPTables(ctx, nw.Bridge, nw.Subnet); err != nil {
		return fmt.Errorf("set up iptables rules: %w", err)
	}

	if err := h.ensureGatewayAccess(ctx, nw); err != nil {
		return fmt.Errorf("let guests reach gateway %s: %w", nw.Gateway, err)
	}

	return nil
}

// TeardownBridge removes a network's bridge, with its iptables rules and its
// gateway access. Best-effort: it logs failures rather than returning them.
func (h *Host) TeardownBridge(ctx context.Context, nw *types.Network) {
	h.mu.Lock()
	delete(h.networks, nw.Bridge)
	h.mu.Unlock()

	h.removeGatewayAccess(ctx, nw)
	if err := deleteBridge(nw.Bridge); err != nil {
		h.logger.WarnContext(ctx, "failed to delete bridge",
			"bridge", nw.Bridge, "error", err)
	}
	h.teardownIPTables(ctx, nw.Bridge)

	h.logger.InfoContext(ctx, "bridge torn down", "network_id", nw.ID, "bridge", nw.Bridge)
}

// ensureBridge creates the network's bridge if it does not exist, and makes
// sure it is up and holds the gateway address.
func (h *Host) ensureBridge(ctx context.Context, nw *types.Network, ipNet *net.IPNet) error {
	br, err := netlink.LinkByName(nw.Bridge)
	switch {
	case isLinkNotFound(err):
		h.logger.InfoContext(ctx, "setting up bridge", "network_id", nw.ID, "bridge", nw.Bridge)
		br = &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: nw.Bridge}}
		if err := netlink.LinkAdd(br); err != nil {
			return fmt.Errorf("create bridge %s: %w", nw.Bridge, err)
		}
	case err != nil:
		return fmt.Errorf("look up bridge %s: %w", nw.Bridge, err)
	}

	if err := netlink.LinkSetUp(br); err != nil {
		return fmt.Errorf("set bridge %s up: %w", nw.Bridge, err)
	}

	addr := &netlink.Addr{
		IPNet: &net.IPNet{IP: net.ParseIP(nw.Gateway), Mask: ipNet.Mask},
	}
	if err := netlink.AddrReplace(br, addr); err != nil {
		return fmt.Errorf("add gateway to bridge %s: %w", nw.Bridge, err)
	}

	return nil
}

// deleteBridge deletes the named bridge, if it exists.
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
