// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"errors"
	"fmt"
	"hash/fnv"

	gotc "github.com/florianl/go-tc"
	"github.com/florianl/go-tc/core"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Traffic control constants.
const (
	ethPAll = 0x0003 // ETH_P_ALL: match all protocols in tc filters

	// kernelHZ is the assumed kernel timer frequency used for TBF burst sizing.
	// 250 Hz is the most common default across Linux distributions.
	kernelHZ = 250

	// minBurstBytes is the minimum TBF burst bucket (1500-byte MTU + Ethernet overhead).
	minBurstBytes = 1540
)

// tcClassID returns a stable tc minor class ID derived from a TAP device name.
// TC class IDs are constrained to 16-bit minor values; we XOR-fold the full
// 32-bit FNV-1a hash to use all available entropy within that space.
func tcClassID(tap string) uint16 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(tap)) // hash.Hash.Write never returns an error
	sum := h.Sum32()
	minor := uint16(sum>>16) ^ uint16(sum)
	if minor == 0 {
		minor = 1 // 0 is reserved (unclassified traffic)
	}
	return minor
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
		return fmt.Errorf("get bridge %s: %w", bridge, err)
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

// limitUpload adds a per-TAP upload rate limit on the bridge. It creates an
// HTB leaf class, an fq_codel sub-qdisc for latency optimization, and a
// flower filter matching traffic arriving from the TAP device.
func limitUpload(bridge, tap string, classID uint16, rateBps, ceilBps int64) error {
	link, err := netlink.LinkByName(bridge)
	if err != nil {
		return fmt.Errorf("get bridge %s: %w", bridge, err)
	}
	ifindex := uint32(link.Attrs().Index)

	rtnl, err := gotc.Open(&gotc.Config{})
	if err != nil {
		return fmt.Errorf("open tc: %w", err)
	}
	defer func() { _ = rtnl.Close() }()

	classHandle := core.BuildHandle(0x1, uint32(classID))
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

	// fq_codel sub-qdisc for better latency under load (best-effort).
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
			Info:    core.FilterInfo(1, ethPAll),
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

// removeUploadLimit removes the per-TAP upload rate limit from the bridge.
// It tears down the flower filter, fq_codel sub-qdisc, and HTB leaf class.
// Idempotent: returns nil if the bridge is gone or no upload limit exists.
func removeUploadLimit(bridge string, classID uint16) error {
	link, err := netlink.LinkByName(bridge)
	if err != nil {
		return nil // bridge already gone; nothing to remove
	}
	ifindex := uint32(link.Attrs().Index)

	rtnl, err := gotc.Open(&gotc.Config{})
	if err != nil {
		return fmt.Errorf("open tc: %w", err)
	}
	defer func() { _ = rtnl.Close() }()

	classHandle := core.BuildHandle(0x1, uint32(classID))

	// Check whether the leaf class exists; if not, no upload limit was set.
	classes, err := rtnl.Class().Get(&gotc.Msg{
		Family:  unix.AF_UNSPEC,
		Ifindex: ifindex,
	})
	if err != nil {
		return nil // no tc hierarchy on this bridge
	}
	classExists := false
	for _, c := range classes {
		if c.Handle == classHandle {
			classExists = true
			break
		}
	}
	if !classExists {
		return nil
	}

	var errs []error

	// Remove flower filter(s) that reference this class.
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

	// Remove fq_codel sub-qdisc.
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

	// Remove leaf class.
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

// limitDownload attaches a TBF qdisc to the TAP device's egress to cap
// the download rate seen by the guest.
func limitDownload(tap string, rateBps int64, burstMultiplier int) error {
	link, err := netlink.LinkByName(tap)
	if err != nil {
		return fmt.Errorf("get device %s: %w", tap, err)
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
