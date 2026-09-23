// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"context"
	"fmt"
	"os"

	"github.com/vishvananda/netlink"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/network"
)

// CreateTAP creates a TAP device for a pre-allocated network allocation
// (IP and MAC already assigned by internal/network). The TAP is attached to
// the network's bridge and bandwidth limits are applied if configured.
func (h *Host) CreateTAP(
	ctx context.Context, nw *dicer.Network, alloc *dicer.NetworkAllocation, bw network.Bandwidth,
) error {
	tap := network.TAPName(alloc.InstanceID)

	// Remove a stale device of the same name if one exists.
	if _, err := netlink.LinkByName(tap); err == nil {
		if err := removeUploadLimit(nw.Bridge, tcClassID(tap)); err != nil {
			h.logger.WarnContext(ctx, "failed to remove stale upload limit",
				"bridge", nw.Bridge, "tap", tap, "error", err)
		}
		if err := deleteTAP(tap); err != nil {
			return fmt.Errorf("delete existing TAP: %w", err)
		}
	}

	if err := createTAP(tap, nw.Bridge, nw.Isolated); err != nil {
		_ = deleteTAP(tap)
		return fmt.Errorf("create TAP device: %w", err)
	}

	if bw.DownloadBps > 0 {
		if err := limitDownload(tap, bw.DownloadBps, h.config.DownloadBurstMultiplier); err != nil {
			return fmt.Errorf("apply download limit: %w", err)
		}
	}

	if bw.UploadBps > 0 {
		burstBps := bw.UploadBurstBps
		if burstBps <= 0 {
			burstBps = bw.UploadBps * int64(h.config.UploadBurstMultiplier)
		}
		if err := limitUpload(nw.Bridge, tap, tcClassID(tap), bw.UploadBps, burstBps); err != nil {
			return fmt.Errorf("apply upload limit: %w", err)
		}
	}

	return nil
}

// RemoveTAP removes the TAP device and its bandwidth limits for an instance.
// Best-effort: logs failures but does not return an error.
func (h *Host) RemoveTAP(ctx context.Context, nw *dicer.Network, instanceID string) {
	tap := network.TAPName(instanceID)
	if err := removeUploadLimit(nw.Bridge, tcClassID(tap)); err != nil {
		h.logger.WarnContext(ctx, "failed to remove upload limit",
			"network_id", nw.ID, "instance_id", instanceID, "tap", tap, "error", err)
	}
	if err := deleteTAP(tap); err != nil {
		h.logger.WarnContext(ctx, "failed to delete TAP device",
			"network_id", nw.ID, "instance_id", instanceID, "tap", tap, "error", err)
	}
}

func createTAP(name, bridge string, isolated bool) error {
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
		return fmt.Errorf("get TAP link %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(tapLink); err != nil {
		return fmt.Errorf("set TAP %s up: %w", name, err)
	}

	br, err := netlink.LinkByName(bridge)
	if err != nil {
		return fmt.Errorf("get bridge %s: %w", bridge, err)
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
