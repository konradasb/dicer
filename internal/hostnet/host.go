// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Package hostnet configures the host networking a VM needs: bridges, TAP
// devices, iptables rules for NAT, isolation and published ports, the
// bridges' places in firewalld, and bandwidth limits. Networks are
// host-local.
package hostnet

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/vishvananda/netlink"

	"github.com/konradasb/dicer/internal/types"
)

// DefaultBurstMultiplier is the upload and download burst multiplier used
// when none is configured.
const DefaultBurstMultiplier = 4

// Config configures a [Host].
type Config struct {
	// UplinkInterface is the interface NAT traffic leaves by. Empty detects
	// it from the default IPv4 route.
	UplinkInterface string
	// UploadBurstMultiplier and DownloadBurstMultiplier are how far an
	// instance may briefly exceed its rate limits, as multiples of them.
	// Less than 1 is DefaultBurstMultiplier.
	UploadBurstMultiplier   int
	DownloadBurstMultiplier int

	// APIPort is the TCP port the daemon serves its API on. Guests are kept
	// from reaching it on any of the host's addresses, since whoever reaches
	// the API controls the host. 0 is none.
	APIPort int

	// Logger is the logger to use. Nil is slog.Default.
	Logger *slog.Logger
}

func (c *Config) applyDefaults() {
	if c.UploadBurstMultiplier < 1 {
		c.UploadBurstMultiplier = DefaultBurstMultiplier
	}
	if c.DownloadBurstMultiplier < 1 {
		c.DownloadBurstMultiplier = DefaultBurstMultiplier
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Host configures this machine's networking on behalf of guests.
type Host struct {
	config Config
	logger *slog.Logger

	// firewalld is nil where there is no system bus to reach it on.
	firewalld *firewalld

	// rulesMu serialises check-then-edit changes to iptables rules.
	rulesMu sync.Mutex

	// mu guards networks, those whose bridges are set up, by bridge.
	mu       sync.Mutex
	networks map[string]types.Network
}

// NewHost creates a host network configurator. Close releases it.
func NewHost(cfg Config) *Host {
	cfg.applyDefaults()

	h := &Host{
		config:   cfg,
		logger:   cfg.Logger.With("component", "hostnet"),
		networks: make(map[string]types.Network),
	}
	var err error
	if h.firewalld, err = connectFirewalld(); err != nil {
		h.logger.Debug("not managing firewalld", "error", err)
	}
	return h
}

// Close releases the host's connection to firewalld.
func (h *Host) Close() {
	if h.firewalld != nil {
		h.firewalld.close()
	}
}

// Subnets returns the IPv4 subnets the host's interfaces are on, as their
// routes in the main table say: its LANs, and Dicer's own bridges that are
// up.
func Subnets() ([]netip.Prefix, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}

	var subnets []netip.Prefix
	for _, route := range routes {
		if route.Scope != netlink.SCOPE_LINK || route.Dst == nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(route.Dst.IP.To4())
		if !ok {
			continue
		}
		ones, _ := route.Dst.Mask.Size()
		subnets = append(subnets, netip.PrefixFrom(addr, ones).Masked())
	}
	return subnets, nil
}

// uplink returns the name of the interface NAT traffic leaves by: the
// configured one, or else the default IPv4 route's.
func (h *Host) uplink() (string, error) {
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
			return "", fmt.Errorf("look up link %d: %w", route.LinkIndex, err)
		}

		return link.Attrs().Name, nil
	}

	return "", ErrNoDefaultRoute
}
