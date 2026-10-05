// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import "errors"

var (
	// ErrInvalidSubnet is returned for a network whose subnet is not a CIDR.
	ErrInvalidSubnet = errors.New("invalid subnet configuration")

	// ErrForwardingDisabled is returned when the host does not forward IPv4.
	ErrForwardingDisabled = errors.New(
		"IPv4 forwarding is not enabled: turn it on with sysctl -w net.ipv4.ip_forward=1")

	// ErrNoDefaultRoute is returned when no uplink is configured and there
	// is no default route to find one by.
	ErrNoDefaultRoute = errors.New(
		"no default route found for uplink detection: set network.uplink_interface in the configuration")
)
