// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/konradasb/dicer/internal/guest"
)

// configureNetwork brings up the guest's interfaces, adds its routes and
// writes its resolv.conf, as cfg describes.
func configureNetwork(log *slog.Logger, cfg *guest.Config) error {
	if err := runIP("link", "set", "lo", "up"); err != nil {
		return fmt.Errorf("bring up lo: %w", err)
	}
	for _, networkInterface := range cfg.Network.Interfaces {
		if err := configureNetworkInterface(networkInterface); err != nil {
			return fmt.Errorf("interface %s: %w", networkInterface.Name, err)
		}
		log.Info("interface configured", "interface", networkInterface.Name,
			"addresses", networkInterface.Addresses, "mtu", networkInterface.MTU)
	}
	for _, route := range cfg.Network.Routes {
		if err := addNetworkRoute(route); err != nil {
			return fmt.Errorf("add route %s: %w", route.Destination, err)
		}
		log.Info("route configured", "destination", route.Destination, "gateway", route.Gateway,
			"device", route.Device, "table", route.Table)
	}
	return writeResolvConf(cfg.Network.DNS)
}

// configureNetworkInterface assigns an interface its addresses and brings it
// up.
func configureNetworkInterface(networkInterface guest.NetworkInterface) error {
	for _, address := range networkInterface.Addresses {
		if err := runIP("addr", "add", address, "dev", networkInterface.Name); err != nil {
			return fmt.Errorf("add address %s: %w", address, err)
		}
	}
	if err := runIP("link", "set", networkInterface.Name, "up"); err != nil {
		return fmt.Errorf("bring up: %w", err)
	}
	return nil
}

// addNetworkRoute adds route to the routing table it names, or to the main
// one.
func addNetworkRoute(route guest.NetworkRoute) error {
	args := []string{"route", "add", route.Destination}
	if route.Gateway != "" {
		args = append(args, "via", route.Gateway)
	}
	if route.Device != "" {
		args = append(args, "dev", route.Device)
	}
	if route.Table != "" {
		args = append(args, "table", route.Table)
	}
	return runIP(args...)
}

// writeResolvConf writes the guest's resolv.conf, unless dns names no
// nameservers.
func writeResolvConf(dns guest.DNSConfig) error {
	if len(dns.Nameservers) == 0 {
		return nil
	}

	var b strings.Builder
	if len(dns.SearchDomains) > 0 {
		fmt.Fprintf(&b, "search %s\n", strings.Join(dns.SearchDomains, " "))
	}
	for _, nameserver := range dns.Nameservers {
		fmt.Fprintf(&b, "nameserver %s\n", nameserver)
	}

	if err := os.MkdirAll(overlayRoot+"/etc", 0o755); err != nil {
		return fmt.Errorf("mkdir /etc: %w", err)
	}
	if err := os.WriteFile(overlayRoot+"/etc/resolv.conf", []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}
	return nil
}

// runIP runs /sbin/ip with args. Its error includes what ip printed.
func runIP(args ...string) error {
	out, err := exec.Command("/sbin/ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}
