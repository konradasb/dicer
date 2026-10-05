// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package network

import (
	crand "crypto/rand"
	"math/rand/v2"
	"net"

	"github.com/konradasb/dicer/internal/errdefs"
)

// ErrNoAvailableIPs is returned when a network's subnet has no address left
// to give out.
var ErrNoAvailableIPs = errdefs.ResourceExhausted("the network has no free addresses left")

// randomMAC returns a random locally administered unicast MAC address.
func randomMAC() (string, error) {
	mac := make(net.HardwareAddr, 6)
	if _, err := crand.Read(mac); err != nil {
		return "", err
	}
	mac[0] = (mac[0] & 0xFE) | 0x02 // locally administered, unicast
	return mac.String(), nil
}

// incrementIP returns the IPv4 address n after ip, or ip itself if it is
// not IPv4.
func incrementIP(ip net.IP, n int) net.IP {
	ip4 := ip.To4()
	if ip4 == nil {
		return ip
	}
	value := uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
	value += uint32(n)
	return net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value)).To4()
}

// freeIP returns an address of ipNet not in used, other than its network
// and broadcast addresses, or ErrNoAvailableIPs if there is none. It tries a
// few at random before scanning, so that addresses are not handed out in
// order.
func freeIP(ipNet *net.IPNet, used map[string]struct{}) (string, error) {
	ones, bits := ipNet.Mask.Size()
	if bits == 0 || bits-ones < 2 {
		// A /31 or /32 has no assignable host addresses.
		return "", ErrNoAvailableIPs
	}
	lastHost := 1<<(bits-ones) - 2 // one before the broadcast address

	// From offset 1: the gateway is usually there, and in used, but
	// including it costs at most one attempt and leaves small subnets a
	// candidate.
	for range 5 {
		// Not a security decision: this only avoids a sequential scan.
		// randomMAC, which does need unpredictability, uses crypto/rand.
		offset := rand.IntN(lastHost) + 1 //nolint:gosec // see above
		ip := incrementIP(ipNet.IP, offset).String()
		if _, taken := used[ip]; !taken {
			return ip, nil
		}
	}

	for offset := 1; offset <= lastHost; offset++ {
		ip := incrementIP(ipNet.IP, offset).String()
		if _, taken := used[ip]; !taken {
			return ip, nil
		}
	}
	return "", ErrNoAvailableIPs
}
