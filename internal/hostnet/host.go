// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Package hostnet configures the host networking a VM needs: bridges, TAP
// devices, iptables NAT rules and traffic shaping. Networks are host-local.
package hostnet

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/vishvananda/netlink"
)

// DefaultBurstMultiplier is the upload and download burst multiplier used
// when none is configured.
const DefaultBurstMultiplier = 4

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

	// rulesMu serialises check-then-edit changes to iptables rules.
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
