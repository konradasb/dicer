// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"errors"
	"fmt"
	"hash/fnv"
	"slices"

	gotc "github.com/florianl/go-tc"
	"github.com/florianl/go-tc/core"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	// ethernetProtocolAll is ETH_P_ALL: a tc filter for it matches every
	// protocol.
	ethernetProtocolAll = 0x0003

	// kernelHZ is the kernel timer frequency TBF bursts are sized for:
	// 250 Hz, the most common default across Linux distributions.
	kernelHZ = 250

	// minBurstBytes is the smallest TBF burst bucket: a 1500-byte MTU and
	// Ethernet overhead.
	minBurstBytes = 1540
)

// uploadClassHandle returns the handle of the HTB class limiting a TAP
// device's upload, stable for its name. A class's minor number is 16 bits,
// so the name's 32-bit FNV-1a hash is XOR-folded to keep all its entropy.
func uploadClassHandle(tap string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(tap)) // hash.Hash.Write never returns an error
	sum := h.Sum32()
	minor := uint16(sum>>16) ^ uint16(sum)
	if minor == 0 {
		minor = 1 // 0 is reserved (unclassified traffic)
	}
	return core.BuildHandle(0x1, uint32(minor))
}

// ensureBridgeQdisc creates the root HTB qdisc and class on a bridge, for
// per-TAP upload limits. It is idempotent, and does nothing for a
// non-positive capacityBps.
func ensureBridgeQdisc(bridge string, capacityBps int64) error {
	if capacityBps <= 0 {
		return nil
	}

	link, err := netlink.LinkByName(bridge)
	if err != nil {
		return fmt.Errorf("look up bridge %s: %w", bridge, err)
	}
	ifindex := uint32(link.Attrs().Index)

	rtnl, err := gotc.Open(&gotc.Config{})
	if err != nil {
		return fmt.Errorf("open tc: %w", err)
	}
	defer func() { _ = rtnl.Close() }()

	qdiscs, err := rtnl.Qdisc().Get()
	if err == nil {
		for _, q := range qdiscs {
			if q.Ifindex == ifindex && q.Kind == "htb" {
				return nil
			}
		}
	}

	if err := rtnl.Qdisc().Add(&gotc.Object{
		Msg: gotc.Msg{
			Family:  unix.AF_UNSPEC,
			Ifindex: ifindex,
			Handle:  core.BuildHandle(0x1, 0x0),
			Parent:  gotc.HandleRoot,
		},
		Attribute: gotc.Attribute{
			Kind: "htb",
			Htb: &gotc.Htb{
				Init: &gotc.HtbGlob{
					Version:      3,
					Rate2Quantum: 10,
				},
			},
		},
	}); err != nil {
		return fmt.Errorf("add HTB root qdisc on %s: %w", bridge, err)
	}

	rateBps := uint64(capacityBps)
	if err := rtnl.Class().Add(&gotc.Object{
		Msg: gotc.Msg{
			Family:  unix.AF_UNSPEC,
			Ifindex: ifindex,
			Handle:  core.BuildHandle(0x1, 0x1),
			Parent:  core.BuildHandle(0x1, 0x0),
		},
		Attribute: gotc.Attribute{
			Kind: "htb",
			Htb: &gotc.Htb{
				Parms: &gotc.HtbOpt{
					Rate: gotc.RateSpec{Rate: uint32(rateBps)},
					Ceil: gotc.RateSpec{Rate: uint32(rateBps)},
				},
				Rate64: &rateBps,
				Ceil64: &rateBps,
			},
		},
	}); err != nil {
		return fmt.Errorf("add HTB root class on %s: %w", bridge, err)
	}

	return nil
}

// limitUpload limits the rate of traffic arriving from a TAP device, with
// an HTB leaf class on the bridge, an fq_codel qdisc under it to keep latency
// low under load, and a flower filter sending the TAP's traffic to it.
func limitUpload(bridge, tap string, rateBps, ceilBps int64) error {
	link, err := netlink.LinkByName(bridge)
	if err != nil {
		return fmt.Errorf("look up bridge %s: %w", bridge, err)
	}
	ifindex := uint32(link.Attrs().Index)

	rtnl, err := gotc.Open(&gotc.Config{})
	if err != nil {
		return fmt.Errorf("open tc: %w", err)
	}
	defer func() { _ = rtnl.Close() }()

	classHandle := uploadClassHandle(tap)
	rate := uint64(rateBps)
	ceil := uint64(ceilBps)

	if err := rtnl.Class().Add(&gotc.Object{
		Msg: gotc.Msg{
			Family:  unix.AF_UNSPEC,
			Ifindex: ifindex,
			Handle:  classHandle,
			Parent:  core.BuildHandle(0x1, 0x1),
		},
		Attribute: gotc.Attribute{
			Kind: "htb",
			Htb: &gotc.Htb{
				Parms: &gotc.HtbOpt{
					Rate: gotc.RateSpec{Rate: uint32(rate)},
					Ceil: gotc.RateSpec{Rate: uint32(ceil)},
					Prio: 1,
				},
				Rate64: &rate,
				Ceil64: &ceil,
			},
		},
	}); err != nil {
		return fmt.Errorf("add HTB leaf class on %s: %w", bridge, err)
	}

	// Best-effort: without it the class still limits the rate.
	_ = rtnl.Qdisc().Add(&gotc.Object{
		Msg: gotc.Msg{
			Family:  unix.AF_UNSPEC,
			Ifindex: ifindex,
			Parent:  classHandle,
		},
		Attribute: gotc.Attribute{
			Kind:    "fq_codel",
			FqCodel: &gotc.FqCodel{},
		},
	})

	flowID := classHandle
	indev := tap
	if err := rtnl.Filter().Add(&gotc.Object{
		Msg: gotc.Msg{
			Family:  unix.AF_UNSPEC,
			Ifindex: ifindex,
			Parent:  core.BuildHandle(0x1, 0x0),
			Info:    core.FilterInfo(1, ethernetProtocolAll),
		},
		Attribute: gotc.Attribute{
			Kind: "flower",
			Flower: &gotc.Flower{
				ClassID: &flowID,
				Indev:   &indev,
			},
		},
	}); err != nil {
		return fmt.Errorf("add flower filter on %s: %w", bridge, err)
	}

	return nil
}

// removeUploadLimit undoes limitUpload. It returns nil if the bridge is
// gone or the TAP device has no upload limit.
func removeUploadLimit(bridge, tap string) error {
	link, err := netlink.LinkByName(bridge)
	if isLinkNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up bridge %s: %w", bridge, err)
	}
	ifindex := uint32(link.Attrs().Index)

	rtnl, err := gotc.Open(&gotc.Config{})
	if err != nil {
		return fmt.Errorf("open tc: %w", err)
	}
	defer func() { _ = rtnl.Close() }()

	classHandle := uploadClassHandle(tap)

	classes, err := rtnl.Class().Get(&gotc.Msg{
		Family:  unix.AF_UNSPEC,
		Ifindex: ifindex,
	})
	if err != nil {
		return nil // no tc hierarchy on this bridge
	}
	if !slices.ContainsFunc(classes, func(c gotc.Object) bool { return c.Handle == classHandle }) {
		return nil
	}

	var errs []error

	filters, err := rtnl.Filter().Get(&gotc.Msg{
		Family:  unix.AF_UNSPEC,
		Ifindex: ifindex,
		Parent:  core.BuildHandle(0x1, 0x0),
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("list filters on %s: %w", bridge, err))
	} else {
		for _, f := range filters {
			if f.Kind != "flower" || f.Flower == nil {
				continue
			}
			if f.Flower.ClassID != nil && *f.Flower.ClassID == classHandle {
				if err := rtnl.Filter().Delete(&gotc.Object{
					Msg:       f.Msg,
					Attribute: gotc.Attribute{Kind: "flower"},
				}); err != nil {
					errs = append(errs, fmt.Errorf("delete flower filter on %s: %w", bridge, err))
				}
			}
		}
	}

	if err := rtnl.Qdisc().Delete(&gotc.Object{
		Msg: gotc.Msg{
			Family:  unix.AF_UNSPEC,
			Ifindex: ifindex,
			Parent:  classHandle,
		},
		Attribute: gotc.Attribute{Kind: "fq_codel"},
	}); err != nil {
		errs = append(errs, fmt.Errorf("delete fq_codel on %s: %w", bridge, err))
	}

	if err := rtnl.Class().Delete(&gotc.Object{
		Msg: gotc.Msg{
			Family:  unix.AF_UNSPEC,
			Ifindex: ifindex,
			Handle:  classHandle,
			Parent:  core.BuildHandle(0x1, 0x1),
		},
		Attribute: gotc.Attribute{Kind: "htb"},
	}); err != nil {
		errs = append(errs, fmt.Errorf("delete HTB class on %s: %w", bridge, err))
	}

	return errors.Join(errs...)
}

// limitDownload limits the rate of traffic sent to a TAP device's guest,
// with a TBF qdisc on the device.
func limitDownload(tap string, rateBps int64, burstMultiplier int) error {
	link, err := netlink.LinkByName(tap)
	if err != nil {
		return fmt.Errorf("look up TAP %s: %w", tap, err)
	}
	ifindex := uint32(link.Attrs().Index)

	rtnl, err := gotc.Open(&gotc.Config{})
	if err != nil {
		return fmt.Errorf("open tc: %w", err)
	}
	defer func() { _ = rtnl.Close() }()

	burstBytes := uint32(max((rateBps*int64(burstMultiplier))/kernelHZ, int64(minBurstBytes)))
	// limit = rate * latency + burst (50ms latency).
	limitBytes := uint32(float64(rateBps)*0.05) + burstBytes

	if err := rtnl.Qdisc().Add(&gotc.Object{
		Msg: gotc.Msg{
			Family:  unix.AF_UNSPEC,
			Ifindex: ifindex,
			Handle:  core.BuildHandle(0x1, 0x0),
			Parent:  gotc.HandleRoot,
		},
		Attribute: gotc.Attribute{
			Kind: "tbf",
			Tbf: &gotc.Tbf{
				Parms: &gotc.TbfQopt{
					Rate:   gotc.RateSpec{Rate: uint32(rateBps)},
					Limit:  limitBytes,
					Buffer: burstBytes,
				},
				Burst: &burstBytes,
			},
		},
	}); err != nil {
		return fmt.Errorf("add TBF qdisc on %s: %w", tap, err)
	}

	return nil
}
