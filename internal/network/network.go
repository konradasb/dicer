// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package network describes host-local virtual networks and assigns guest
// addresses on them.
//
// It holds the address policy, and deliberately has no Linux networking
// dependency, so it builds and tests on any platform. The networks and
// allocations themselves are dicer.Network and dicer.NetworkAllocation;
// creating the bridges and TAP devices those addresses are used on is the job
// of internal/hostnet.
package network

import (
	"net"

	"github.com/dicer-sh/dicer"
)

// Bandwidth limits an instance's traffic. Zero means unlimited.
type Bandwidth struct {
	UploadBps      int64
	UploadBurstBps int64
	DownloadBps    int64
}

// maxPrefixLen is the longest prefix a network may have: a /30 is the
// smallest subnet with an address left for an instance once the network,
// gateway and broadcast addresses are set aside.
const maxPrefixLen = 30

// ParseSubnet parses a network's subnet: an IPv4 CIDR with room for at least
// one instance. The result is normalised, so "10.0.0.5/24" is 10.0.0.0/24.
func ParseSubnet(s string) (*net.IPNet, error) {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, dicer.InvalidArgument("invalid subnet %q: want an IPv4 CIDR such as 172.20.0.0/16", s)
	}
	if ipNet.IP.To4() == nil {
		return nil, dicer.InvalidArgument("subnet %s is not IPv4; only IPv4 networks are supported", ipNet)
	}
	if ones, _ := ipNet.Mask.Size(); ones > maxPrefixLen {
		return nil, dicer.InvalidArgument("subnet %s is too small: the longest prefix a network may have is /%d",
			ipNet, maxPrefixLen)
	}
	return ipNet, nil
}

// Assignable reports whether ip can be given out on ipNet: an address in it
// other than its network and broadcast addresses.
func Assignable(ipNet *net.IPNet, ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil || !ipNet.Contains(ip4) {
		return false
	}
	network := ipNet.IP.To4()
	broadcast := make(net.IP, len(network))
	for i := range network {
		broadcast[i] = network[i] | ^ipNet.Mask[i]
	}
	return !ip4.Equal(network) && !ip4.Equal(broadcast)
}
