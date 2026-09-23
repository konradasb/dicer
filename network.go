// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Network is a bridged network instances attach to, with an address pool of
// its own.
type Network struct {
	ID          string    `yaml:"id"`
	Name        string    `yaml:"name"`
	Gateway     string    `yaml:"gateway"`
	Subnet      string    `yaml:"subnet"`
	Bridge      string    `yaml:"bridge"`
	Nameservers []string  `yaml:"nameservers,omitempty"`
	MTU         int       `yaml:"mtu,omitempty"`
	Isolated    bool      `yaml:"isolated,omitempty"`
	CreatedAt   time.Time `yaml:"created_at"`
	UpdatedAt   time.Time `yaml:"updated_at"`

	// TotalIPs is how many addresses the network has for instances, and
	// FreeIPs how many are unused. They are worked out from the
	// allocations when the network is read, not stored, so they are set on
	// a network the API returns and zero on one being written.
	TotalIPs int64 `yaml:"-"`
	FreeIPs  int64 `yaml:"-"`
}

// NetworkAllocation is one instance's address on a network, held for as long
// as the instance is defined.
type NetworkAllocation struct {
	NetworkID  string `yaml:"network_id"`
	InstanceID string `yaml:"instance_id"`
	IP         string `yaml:"ip"`
	MAC        string `yaml:"mac"`

	// InstanceName is the instance's name, and TAPDevice the host device
	// its traffic passes through. Both are derived when the allocation is
	// read -- the device from the instance ID -- rather than stored, so
	// that a rename cannot leave either stale.
	InstanceName string `yaml:"-"`
	TAPDevice    string `yaml:"-"`
}

// Netmask returns the network's subnet mask in dotted-quad form.
func (n Network) Netmask() (string, error) {
	_, ipnet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet CIDR in network %q: %w", n.ID, err)
	}

	return net.IP(ipnet.Mask).String(), nil
}

// PrefixLen returns the length of the network's subnet prefix.
func (n Network) PrefixLen() (int, error) {
	_, ipnet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return 0, fmt.Errorf("invalid subnet CIDR in network %q: %w", n.ID, err)
	}

	ones, _ := ipnet.Mask.Size()

	return ones, nil
}

// Usage returns how many addresses the network has for instances, and how
// many are left once allocated of them are taken. The network, broadcast and
// gateway addresses are not for instances and are not counted.
func (n Network) Usage(allocated int) (total, free int64) {
	_, ipNet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return 0, 0
	}

	ones, bits := ipNet.Mask.Size()
	if bits == 0 {
		return 0, 0
	}

	total = max((int64(1)<<(bits-ones))-3, 0)
	free = max(total-int64(allocated), 0)

	return total, free
}

// The protocols a port mapping may name.
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// PortMapping publishes a guest port on the host.
type PortMapping struct {
	// HostIP is the host address the port is published on. Empty means
	// every address the host has, except loopback: traffic to 127.0.0.1 is
	// not forwarded to guests.
	HostIP    string `yaml:"host_ip,omitempty" json:"host_ip,omitempty"`
	HostPort  uint16 `yaml:"host_port" json:"host_port"`
	GuestPort uint16 `yaml:"guest_port" json:"guest_port"`

	// Protocol is tcp or udp. Empty means tcp.
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
}

// Proto returns the mapping's protocol, filling in the default.
func (p PortMapping) Proto() string {
	if p.Protocol == "" {
		return ProtocolTCP
	}

	return p.Protocol
}

// String is the mapping as a person writes it: "[hostIP:]hostPort:guestPort/proto".
func (p PortMapping) String() string {
	s := fmt.Sprintf("%d:%d/%s", p.HostPort, p.GuestPort, p.Proto())
	if p.HostIP != "" {
		s = p.HostIP + ":" + s
	}

	return s
}

// Overlaps reports whether two mappings want the same host port, and so
// cannot both be published. A mapping on every address overlaps one on any
// single address.
func (p PortMapping) Overlaps(other PortMapping) bool {
	if p.Proto() != other.Proto() || p.HostPort != other.HostPort {
		return false
	}

	return p.HostIP == "" || other.HostIP == "" || p.HostIP == other.HostIP
}

// ParsePortMapping parses a mapping as a person writes it:
// "8080:80", "127.0.0.1:8080:80", "53:53/udp".
func ParsePortMapping(s string) (PortMapping, error) {
	spec, proto, _ := strings.Cut(s, "/")
	parts := strings.Split(spec, ":")

	var hostIP, hostPort, guestPort string
	switch len(parts) {
	case 2:
		hostPort, guestPort = parts[0], parts[1]
	case 3:
		hostIP, hostPort, guestPort = parts[0], parts[1], parts[2]
	default:
		return PortMapping{}, InvalidArgument(
			"invalid port mapping %q: want [hostIP:]hostPort:guestPort[/tcp|udp], e.g. 8080:80", s)
	}

	host, err := parsePort(hostPort)
	if err != nil {
		return PortMapping{}, fmt.Errorf("invalid port mapping %q: host port: %w", s, err)
	}
	guest, err := parsePort(guestPort)
	if err != nil {
		return PortMapping{}, fmt.Errorf("invalid port mapping %q: guest port: %w", s, err)
	}

	return PortMapping{HostIP: hostIP, HostPort: host, GuestPort: guest, Protocol: proto}, nil
}

func parsePort(s string) (uint16, error) {
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil || n == 0 {
		return 0, InvalidArgument("invalid port %q: want a number from 1 to 65535", s)
	}

	return uint16(n), nil
}

// ValidatePorts reports whether every mapping can be published, and that no
// two of them want the same host port.
func ValidatePorts(ports []PortMapping) error {
	for i, p := range ports {
		switch {
		case p.HostPort == 0:
			return InvalidArgument("invalid port mapping %s: no host port", p)
		case p.GuestPort == 0:
			return InvalidArgument("invalid port mapping %s: no guest port", p)
		case p.Proto() != ProtocolTCP && p.Proto() != ProtocolUDP:
			return InvalidArgument("invalid port mapping %s: the protocol must be tcp or udp", p)
		}

		if p.HostIP != "" {
			ip := net.ParseIP(p.HostIP)
			switch {
			case ip == nil || ip.To4() == nil:
				return InvalidArgument("invalid port mapping %s: %q is not an IPv4 address", p, p.HostIP)
			case ip.IsLoopback():
				return InvalidArgument("cannot publish port %s on loopback: "+
					"publish it on one of the host's own addresses instead", p)
			}
		}

		for _, prev := range ports[:i] {
			if p.Overlaps(prev) {
				return InvalidArgument("port %s overlaps port %s", p, prev)
			}
		}
	}

	return nil
}
