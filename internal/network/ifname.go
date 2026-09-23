// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package network

import (
	"fmt"
	"hash/fnv"
)

// Host interface names are derived from a resource's ID or name, hashed when
// needed to fit in IFNAMSIZ.

// maxInterfaceName is the longest Linux interface name: IFNAMSIZ less the
// terminating NUL.
const maxInterfaceName = 15

// TAPName returns the TAP device name for an instance: "tap-" followed by
// the 8 hex digits of an FNV-1a hash of its ID. It is derived rather than
// stored, so every holder of an instance ID agrees on it.
func TAPName(instanceID string) string {
	return "tap-" + hash32(instanceID)
}

// BridgeName returns the bridge name for a network: "dicer-<name>" if it
// fits, otherwise "dbr-" and a hash of the name.
func BridgeName(networkName string) string {
	if name := "dicer-" + networkName; len(name) <= maxInterfaceName {
		return name
	}
	return "dbr-" + hash32(networkName)
}

func hash32(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s)) // hash.Hash.Write never returns an error
	return fmt.Sprintf("%08x", h.Sum32())
}
