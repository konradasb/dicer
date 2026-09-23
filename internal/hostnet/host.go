// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Package hostnet configures the host networking a VM needs: bridges, TAP
// devices, iptables NAT rules and traffic shaping. It is the only package
// here that talks to netlink, which is why it is separate from internal/network
// -- that one holds the network domain type and address policy, and stays
// portable.
//
// Networks are host-local by design. Connecting VMs across hosts is a matter
// of connecting the hosts themselves -- WireGuard, a VPN, a routed fabric --
// and letting these bridges route over it. The daemon does not participate.
package hostnet

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/vishvananda/netlink"
)

const (
	defaultUploadBurstMultiplier   = 4
	defaultDownloadBurstMultiplier = 4
)

// Config configures a [Host].
type Config struct {
	UplinkInterface         string
	UplinkCapacityBps       int64
	UploadBurstMultiplier   int
	DownloadBurstMultiplier int

	Logger *slog.Logger
}

func (c *Config) applyDefaults() {
	if c.UploadBurstMultiplier < 1 {
		c.UploadBurstMultiplier = defaultUploadBurstMultiplier
	}
	if c.DownloadBurstMultiplier < 1 {
		c.DownloadBurstMultiplier = defaultDownloadBurstMultiplier
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Host configures this machine's networking on behalf of guests.
type Host struct {
	config Config
	logger *slog.Logger

	// rulesMu serialises this daemon's changes to its iptables rules. Each
	// change is a check followed by an edit, and two made at once -- two
	// networks' bridges coming up together -- would both find a jump
	// missing and both insert it.
	rulesMu sync.Mutex
}

// NewHost creates a host network configurator.
func NewHost(cfg Config) *Host {
	cfg.applyDefaults()

	return &Host{
		config: cfg,
		logger: cfg.Logger.With("component", "hostnet"),
	}
}

// resolveUplink returns the configured uplink interface name, or detects it
// from the default IPv4 route when not explicitly configured.
func (h *Host) resolveUplink() (string, error) {
	if h.config.UplinkInterface != "" {
		return h.config.UplinkInterface, nil
	}

	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return "", fmt.Errorf("list routes: %w", err)
	}

	for _, route := range routes {
		if route.Dst != nil && !route.Dst.IP.IsUnspecified() {
			continue
		}

		link, err := netlink.LinkByIndex(route.LinkIndex)
		if err != nil {
			return "", fmt.Errorf("resolve link index %d: %w", route.LinkIndex, err)
		}

		return link.Attrs().Name, nil
	}

	return "", ErrNoDefaultRoute
}
