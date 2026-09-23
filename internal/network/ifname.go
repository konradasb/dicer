// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package network

import (
	"fmt"
	"hash/fnv"
)

// Host interface names are derived from a resource's ID or name rather than
// stored, so that every holder of the ID agrees on them without looking
// anything up, and a daemon that died mid-operation can still find what it
// created. They must fit in IFNAMSIZ, which is why a name that would not is
// hashed instead.

// maxInterfaceName is the longest Linux interface name: IFNAMSIZ less the
// terminating NUL.
const maxInterfaceName = 15

// TAPName returns the TAP device name for an instance: "tap-" followed by
// the 8 hex digits of an FNV-1a hash of its ID. It is derived rather than
// stored, so every holder of an instance ID agrees on it.
func TAPName(instanceID string) string {
	return "tap-" + hash32(instanceID)
}

// BridgeName returns the Linux bridge name for a network: "dicer-<name>" when
// that fits in an interface name, or "dbr-" and a hash of the name when it
// does not. Truncating instead would give "production-a" and "production-b"
// the same bridge. The two prefixes differ, so a hashed name cannot collide
// with a literal one.
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
