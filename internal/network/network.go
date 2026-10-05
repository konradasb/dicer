// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package network assigns guest addresses on host-local networks and names
// their interfaces. It is portable; internal/hostnet configures the host.
package network

import (
	"net"

	"github.com/konradasb/dicer/internal/errdefs"
)

// Defaults applied to a network that does not specify them.
const (
	// DefaultNameserver is the nameserver a network forwards to.
	DefaultNameserver = "8.8.8.8"
	// DefaultMTU is a network's MTU.
	DefaultMTU = 1500
)

// Bandwidth limits the bytes per second an instance's guest sends and
// receives. Zero is unlimited.
type Bandwidth struct {
	UploadBytesPerSecond   int64
	DownloadBytesPerSecond int64
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
		return nil, errdefs.InvalidArgument("invalid subnet %q: want an IPv4 CIDR such as 172.20.0.0/16", s)
	}
	if ipNet.IP.To4() == nil {
		return nil, errdefs.InvalidArgument("subnet %s is not IPv4; only IPv4 networks are supported", ipNet)
	}
	if ones, _ := ipNet.Mask.Size(); ones > maxPrefixLen {
		return nil, errdefs.InvalidArgument("subnet %s is too small: the longest prefix a network may have is /%d",
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
	networkIP := ipNet.IP.To4()
	broadcastIP := make(net.IP, len(networkIP))
	for i := range networkIP {
		broadcastIP[i] = networkIP[i] | ^ipNet.Mask[i]
	}
	return !ip4.Equal(networkIP) && !ip4.Equal(broadcastIP)
}
