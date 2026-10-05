// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// SetIdentity implements diceragentv1.AgentServiceServer. It works through
// netlink rather than ip(8), which a guest's image need not have.
func (s *server) SetIdentity(
	_ context.Context, req *diceragentv1.SetIdentityRequest,
) (*diceragentv1.SetIdentityResponse, error) {
	for _, networkInterface := range req.GetInterfaces() {
		if err := configureInterface(networkInterface); err != nil {
			return nil, status.Errorf(codes.Internal, "interface %s: %v", networkInterface.GetName(), err)
		}
	}
	// After the interfaces: a route needs its gateway reachable.
	for _, route := range req.GetRoutes() {
		if err := replaceRoute(route); err != nil {
			return nil, status.Errorf(codes.Internal, "route %s: %v", route.GetDestination(), err)
		}
	}

	if nameservers := req.GetNameservers(); len(nameservers) > 0 {
		var b strings.Builder
		for _, nameserver := range nameservers {
			fmt.Fprintf(&b, "nameserver %s\n", nameserver)
		}
		if err := os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644); err != nil {
			return nil, status.Errorf(codes.Internal, "write resolv.conf: %v", err)
		}
	}

	if hostname := req.GetHostname(); hostname != "" {
		if err := syscall.Sethostname([]byte(hostname)); err != nil {
			return nil, status.Errorf(codes.Internal, "set hostname: %v", err)
		}
		if err := os.WriteFile("/etc/hostname", []byte(hostname+"\n"), 0o644); err != nil {
			return nil, status.Errorf(codes.Internal, "write /etc/hostname: %v", err)
		}
	}

	return &diceragentv1.SetIdentityResponse{}, nil
}

// configureInterface gives an interface its MAC address and MTU, and its
// IPv4 addresses in place of those it has. Taking it down for the MAC to
// change drops its routes, and its IPv6 link-local address, which comes back
// from the new MAC as it comes up.
func configureInterface(want *diceragentv1.NetworkInterface) error {
	link, err := netlink.LinkByName(want.GetName())
	if err != nil {
		return err
	}
	mac, err := net.ParseMAC(want.GetMac())
	if err != nil {
		return err
	}

	if err := netlink.LinkSetDown(link); err != nil {
		return fmt.Errorf("take down: %w", err)
	}
	if err := netlink.LinkSetHardwareAddr(link, mac); err != nil {
		return fmt.Errorf("set MAC %s: %w", mac, err)
	}
	if mtu := int(want.GetMtu()); mtu > 0 {
		if err := netlink.LinkSetMTU(link, mtu); err != nil {
			return fmt.Errorf("set MTU %d: %w", mtu, err)
		}
	}

	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("list addresses: %w", err)
	}
	for _, address := range addresses {
		if err := netlink.AddrDel(link, &address); err != nil {
			return fmt.Errorf("remove address %s: %w", address.IPNet, err)
		}
	}
	for _, cidr := range want.GetAddresses() {
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			return err
		}
		if err := netlink.AddrAdd(link, address); err != nil {
			return fmt.Errorf("add address %s: %w", cidr, err)
		}
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring up: %w", err)
	}
	return nil
}

// replaceRoute routes a destination through a gateway, replacing any route
// to it.
func replaceRoute(want *diceragentv1.NetworkRoute) error {
	gateway := net.ParseIP(want.GetGateway())
	if gateway == nil {
		return fmt.Errorf("invalid gateway %q", want.GetGateway())
	}

	route := &netlink.Route{Gw: gateway}
	if destination := want.GetDestination(); destination != "default" {
		_, network, err := net.ParseCIDR(destination)
		if err != nil {
			return err
		}
		route.Dst = network
	}
	return netlink.RouteReplace(route)
}
