// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import "errors"

var (
	// ErrInvalidSubnet is returned for invalid subnet configuration.
	ErrInvalidSubnet = errors.New("invalid subnet configuration")

	// ErrForwardingDisabled is returned when IP forwarding is not enabled.
	ErrForwardingDisabled = errors.New("IPv4 forwarding is not enabled")

	// ErrNoDefaultRoute is returned when uplink detection fails.
	ErrNoDefaultRoute = errors.New("no default route found for uplink detection")
)
