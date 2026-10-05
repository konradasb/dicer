// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"fmt"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	// kernelHZ is the kernel timer frequency TBF bursts are sized for:
	// 250 Hz, the most common default across Linux distributions.
	kernelHZ = 250

	// minBurstBytes is the smallest TBF burst bucket: a 1500-byte MTU and
	// Ethernet overhead.
	minBurstBytes = 1540
)

// ingressHandle is the handle of a device's ingress qdisc, ffff:.
var ingressHandle = netlink.MakeHandle(0xffff, 0)

// limitUpload limits the rate of traffic a TAP device's guest sends. That
// traffic arrives on the device's ingress, where nothing can be queued, so
// it is redirected to the IFB device ifb and shaped as it leaves that. The
// IFB device is shaped before anything is redirected to it, so no traffic
// passes unshaped.
func limitUpload(tap, ifb string, rateBps int64, burstMultiplier int) error {
	tapLink, err := netlink.LinkByName(tap)
	if err != nil {
		return fmt.Errorf("look up TAP %s: %w", tap, err)
	}

	attrs := netlink.NewLinkAttrs()
	attrs.Name = ifb
	if err := netlink.LinkAdd(&netlink.Ifb{LinkAttrs: attrs}); err != nil {
		return fmt.Errorf("create IFB %s: %w", ifb, err)
	}
	ifbLink, err := netlink.LinkByName(ifb)
	if err != nil {
		return fmt.Errorf("look up IFB %s: %w", ifb, err)
	}
	if err := netlink.LinkSetUp(ifbLink); err != nil {
		return fmt.Errorf("set IFB %s up: %w", ifb, err)
	}
	if err := limitEgressRate(ifb, rateBps, burstMultiplier); err != nil {
		return err
	}

	if err := netlink.QdiscAdd(&netlink.Ingress{QdiscAttrs: netlink.QdiscAttrs{
		LinkIndex: tapLink.Attrs().Index,
		Handle:    ingressHandle,
		Parent:    netlink.HANDLE_INGRESS,
	}}); err != nil {
		return fmt.Errorf("add ingress qdisc on %s: %w", tap, err)
	}
	if err := netlink.FilterAdd(&netlink.MatchAll{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: tapLink.Attrs().Index,
			Parent:    ingressHandle,
			Priority:  1,
			Protocol:  unix.ETH_P_ALL,
		},
		Actions: []netlink.Action{netlink.NewMirredAction(ifbLink.Attrs().Index)},
	}); err != nil {
		return fmt.Errorf("redirect %s to %s: %w", tap, ifb, err)
	}

	return nil
}

// removeUploadLimit deletes the IFB device limitUpload shapes a TAP device's
// upload with, if it exists. The ingress qdisc redirecting to it goes with
// the TAP device.
func removeUploadLimit(ifb string) error {
	link, err := netlink.LinkByName(ifb)
	if isLinkNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up IFB %s: %w", ifb, err)
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("delete IFB %s: %w", ifb, err)
	}

	return nil
}

// limitEgressRate limits the rate of traffic leaving a device with a TBF
// qdisc: on a TAP device, the rate its guest downloads at, and on an IFB
// device the rate it uploads at. Its bucket lets the traffic briefly reach
// burstMultiplier times the rate.
func limitEgressRate(device string, rateBps int64, burstMultiplier int) error {
	link, err := netlink.LinkByName(device)
	if err != nil {
		return fmt.Errorf("look up %s: %w", device, err)
	}

	rate := uint64(rateBps)
	burstBytes := uint32(max(rateBps*int64(burstMultiplier)/kernelHZ, minBurstBytes))
	// Queue what the rate sends in 50ms, on top of the burst.
	limitBytes := uint32(rateBps/20) + burstBytes

	if err := netlink.QdiscAdd(&netlink.Tbf{
		QdiscAttrs: netlink.QdiscAttrs{
			LinkIndex: link.Attrs().Index,
			Handle:    netlink.MakeHandle(1, 0),
			Parent:    netlink.HANDLE_ROOT,
		},
		Rate:  rate,
		Limit: limitBytes,
		// The kernel takes the bucket as the time the rate takes to fill it.
		Buffer: netlink.Xmittime(rate, burstBytes),
	}); err != nil {
		return fmt.Errorf("add TBF qdisc on %s: %w", device, err)
	}

	return nil
}
