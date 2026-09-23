// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package network

import (
	crand "crypto/rand"
	"fmt"
	"math/rand/v2"
	"net"

	"github.com/konradasb/dicer/internal/errdefs"
)

// Defaults applied to a network that does not specify them.
const (
	DefaultNameserver = "8.8.8.8"
	DefaultMTU        = 1500
)

// ErrNoAvailableIPs is returned when the subnet is exhausted.
var ErrNoAvailableIPs = errdefs.ResourceExhausted("the network has no free addresses left")

// randomMAC returns a random locally-administered unicast MAC address.
func randomMAC() (string, error) {
	b := make([]byte, 6)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	b[0] = (b[0] & 0xFE) | 0x02 // locally administered, unicast
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5]), nil
}

// incrementIP increments an IPv4 address by n.
func incrementIP(ip net.IP, n int) net.IP {
	ip4 := ip.To4()
	if ip4 == nil {
		return ip
	}
	result := make(net.IP, 4)
	copy(result, ip4)
	val := uint32(result[0])<<24 | uint32(result[1])<<16 | uint32(result[2])<<8 | uint32(result[3])
	val += uint32(n)
	result[0] = byte(val >> 24)
	result[1] = byte(val >> 16)
	result[2] = byte(val >> 8)
	result[3] = byte(val)
	return result
}

// allocateIP picks an available IP from ipNet, excluding the network and
// broadcast addresses. It tries random candidates, then scans.
func allocateIP(ipNet *net.IPNet, usedIPs map[string]struct{}) (string, error) {
	ones, bits := ipNet.Mask.Size()
	if bits == 0 || bits-ones < 2 {
		// A /31 or /32 has no assignable host addresses.
		return "", ErrNoAvailableIPs
	}

	subnetSize := 1 << (bits - ones)
	lastHost := subnetSize - 2 // one before the broadcast address

	// Random phase: start at offset 1. The gateway is usually there and is in
	// usedIPs, but including it costs at most one wasted attempt and keeps
	// small subnets from having no candidates at all.
	if hosts := lastHost; hosts >= 1 {
		for range 5 {
			// Not a security decision: this only avoids a sequential scan
			// when picking a free address. MAC generation, which does need
			// unpredictability, uses crypto/rand above.
			offset := rand.IntN(hosts) + 1 //nolint:gosec // see above
			candidate := incrementIP(ipNet.IP, offset)
			s := candidate.String()
			if _, used := usedIPs[s]; !used {
				return s, nil
			}
		}
	}

	// Sequential fallback over the same bounded range.
	for offset := 1; offset <= lastHost; offset++ {
		s := incrementIP(ipNet.IP, offset).String()
		if _, used := usedIPs[s]; !used {
			return s, nil
		}
	}

	return "", ErrNoAvailableIPs
}
