// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"context"
	"fmt"
	"os"

	"github.com/vishvananda/netlink"

	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
)

// CreateTAP creates the TAP device of an instance's network allocation,
// replacing a stale one of the same name, attaches it to the network's
// bridge and applies bw's limits. On failure the device may be left behind
// for RemoveTAP.
func (h *Host) CreateTAP(
	ctx context.Context, nw *types.Network, allocation *types.NetworkAllocation, bw network.Bandwidth,
) error {
	tap := network.TAPName(allocation.InstanceID)
	ifb := network.IFBName(allocation.InstanceID)

	if err := removeUploadLimit(ifb); err != nil {
		return fmt.Errorf("remove stale upload limit: %w", err)
	}
	if err := deleteTAP(tap); err != nil {
		return fmt.Errorf("delete existing TAP: %w", err)
	}

	if err := addTAP(tap, nw.Bridge, nw.Isolated); err != nil {
		_ = deleteTAP(tap)
		return err
	}

	if bw.DownloadBytesPerSecond > 0 {
		if err := limitEgressRate(tap, bw.DownloadBytesPerSecond, h.config.DownloadBurstMultiplier); err != nil {
			return fmt.Errorf("apply download limit: %w", err)
		}
	}

	if bw.UploadBytesPerSecond > 0 {
		if err := limitUpload(tap, ifb, bw.UploadBytesPerSecond, h.config.UploadBurstMultiplier); err != nil {
			return fmt.Errorf("apply upload limit: %w", err)
		}
	}

	return nil
}

// RemoveTAP removes an instance's TAP device and its bandwidth limits.
// Best-effort: it logs failures rather than returning them.
func (h *Host) RemoveTAP(ctx context.Context, nw *types.Network, instanceID string) {
	tap := network.TAPName(instanceID)
	if err := removeUploadLimit(network.IFBName(instanceID)); err != nil {
		h.logger.WarnContext(ctx, "failed to remove upload limit",
			"network_id", nw.ID, "instance_id", instanceID, "tap", tap, "error", err)
	}
	if err := deleteTAP(tap); err != nil {
		h.logger.WarnContext(ctx, "failed to delete TAP device",
			"network_id", nw.ID, "instance_id", instanceID, "tap", tap, "error", err)
	}
}

// addTAP adds a TAP device, owned by this process's user, and attaches it to
// the bridge.
func addTAP(name, bridge string, isolated bool) error {
	uid, gid := os.Getuid(), os.Getgid()
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Owner:     uint32(uid),
		Group:     uint32(gid),
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("create TAP %s: %w", name, err)
	}

	tapLink, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("look up TAP %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(tapLink); err != nil {
		return fmt.Errorf("set TAP %s up: %w", name, err)
	}

	br, err := netlink.LinkByName(bridge)
	if err != nil {
		return fmt.Errorf("look up bridge %s: %w", bridge, err)
	}
	if err := netlink.LinkSetMaster(tapLink, br); err != nil {
		return fmt.Errorf("attach TAP %s to bridge %s: %w", name, bridge, err)
	}
	// An isolated port reaches only the bridge's non-isolated ports -- the
	// gateway -- and not the other instances. Failing to set it would leave
	// the instance quietly unisolated, so it is an error.
	if isolated {
		if err := netlink.LinkSetIsolated(tapLink, true); err != nil {
			return fmt.Errorf("isolate TAP %s: %w", name, err)
		}
	}

	return nil
}

// deleteTAP deletes the named TAP device, if it exists.
func deleteTAP(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		if isLinkNotFound(err) {
			return nil // already gone; deleting is idempotent
		}
		return fmt.Errorf("look up TAP %s: %w", name, err)
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("delete TAP %s: %w", name, err)
	}

	return nil
}
