// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"fmt"
	"net"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
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

	// TotalIPs and FreeIPs count the addresses for instances. They are
	// computed when the network is read and not stored.
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

	// InstanceName and TAPDevice are derived when the allocation is read
	// and not stored.
	InstanceName string `yaml:"-"`
	TAPDevice    string `yaml:"-"`
}

// Netmask returns the network's subnet mask in dotted-quad form.
func (n Network) Netmask() (string, error) {
	subnet, err := n.subnet()
	if err != nil {
		return "", err
	}

	return net.IP(subnet.Mask).String(), nil
}

// PrefixLen returns the length of the network's subnet prefix.
func (n Network) PrefixLen() (int, error) {
	subnet, err := n.subnet()
	if err != nil {
		return 0, err
	}

	ones, _ := subnet.Mask.Size()

	return ones, nil
}

// IPCounts returns how many addresses the network has for instances and how
// many of them are free with allocated taken. The network, broadcast and
// gateway addresses are excluded. Both are zero if the subnet is invalid.
func (n Network) IPCounts(allocated int) (total, free int64) {
	subnet, err := n.subnet()
	if err != nil {
		return 0, 0
	}

	ones, bits := subnet.Mask.Size()
	if bits == 0 {
		return 0, 0
	}

	total = max((int64(1)<<(bits-ones))-3, 0)
	free = max(total-int64(allocated), 0)

	return total, free
}

func (n Network) subnet() (*net.IPNet, error) {
	_, subnet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet CIDR in network %q: %w", n.ID, err)
	}

	return subnet, nil
}

// The protocols a port mapping may name.
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// PortMapping publishes a guest port on the host.
type PortMapping struct {
	// HostIP is the host address to publish on. Empty means every
	// non-loopback address.
	HostIP    string `yaml:"host_ip,omitempty" json:"host_ip,omitempty"`
	HostPort  uint16 `yaml:"host_port" json:"host_port"`
	GuestPort uint16 `yaml:"guest_port" json:"guest_port"`

	// Protocol is tcp or udp. Empty means tcp.
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
}

// EffectiveProtocol returns the mapping's protocol, filling in the default.
func (p PortMapping) EffectiveProtocol() string {
	if p.Protocol == "" {
		return ProtocolTCP
	}

	return p.Protocol
}

// String is the mapping as a person writes it:
// "[hostIP:]hostPort:guestPort/protocol".
func (p PortMapping) String() string {
	s := fmt.Sprintf("%d:%d/%s", p.HostPort, p.GuestPort, p.EffectiveProtocol())
	if p.HostIP != "" {
		s = p.HostIP + ":" + s
	}

	return s
}

// Overlaps reports whether two mappings claim the same host port. A mapping
// on every address overlaps one on any single address.
func (p PortMapping) Overlaps(other PortMapping) bool {
	if p.EffectiveProtocol() != other.EffectiveProtocol() || p.HostPort != other.HostPort {
		return false
	}

	return p.HostIP == "" || other.HostIP == "" || p.HostIP == other.HostIP
}

// ValidatePorts returns an invalid argument error if a mapping is invalid
// or two overlap.
func ValidatePorts(ports []PortMapping) error {
	for i, p := range ports {
		switch {
		case p.HostPort == 0:
			return errdefs.InvalidArgument("invalid port mapping %s: no host port", p)
		case p.GuestPort == 0:
			return errdefs.InvalidArgument("invalid port mapping %s: no guest port", p)
		case p.EffectiveProtocol() != ProtocolTCP && p.EffectiveProtocol() != ProtocolUDP:
			return errdefs.InvalidArgument("invalid port mapping %s: the protocol must be tcp or udp", p)
		}

		if p.HostIP != "" {
			ip := net.ParseIP(p.HostIP)
			switch {
			case ip == nil || ip.To4() == nil:
				return errdefs.InvalidArgument("invalid port mapping %s: %q is not an IPv4 address", p, p.HostIP)
			case ip.IsLoopback():
				return errdefs.InvalidArgument("cannot publish port %s on loopback: "+
					"publish it on one of the host's own addresses instead", p)
			}
		}

		for _, prev := range ports[:i] {
			if p.Overlaps(prev) {
				return errdefs.InvalidArgument("port %s overlaps port %s", p, prev)
			}
		}
	}

	return nil
}
