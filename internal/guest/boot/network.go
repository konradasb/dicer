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
	if err := ipExec("link", "set", "lo", "up"); err != nil {
		return fmt.Errorf("bring up lo: %w", err)
	}
	for _, iface := range cfg.Network.Interfaces {
		if err := configureInterface(iface); err != nil {
			return fmt.Errorf("interface %s: %w", iface.Interface, err)
		}
		log.Info("interface configured", "iface", iface.Interface, "addresses", iface.Addresses, "mtu", iface.MTU)
	}
	for _, r := range cfg.Network.Routes {
		if err := addRoute(r); err != nil {
			return fmt.Errorf("add route %s: %w", r.Destination, err)
		}
		log.Info("route configured", "destination", r.Destination, "gateway", r.Gateway, "dev", r.Dev, "table", r.Table)
	}
	return writeResolvConf(cfg.Network.DNS)
}

// configureInterface assigns an interface its addresses and brings it up.
func configureInterface(iface guest.NetworkInterface) error {
	for _, addr := range iface.Addresses {
		if err := ipExec("addr", "add", addr, "dev", iface.Interface); err != nil {
			return fmt.Errorf("add address %s: %w", addr, err)
		}
	}
	if err := ipExec("link", "set", iface.Interface, "up"); err != nil {
		return fmt.Errorf("bring up: %w", err)
	}
	return nil
}

// addRoute adds a route to the guest's routing table.
func addRoute(r guest.NetworkRoute) error {
	args := []string{"route", "add", r.Destination}
	if r.Gateway != "" {
		args = append(args, "via", r.Gateway)
	}
	if r.Dev != "" {
		args = append(args, "dev", r.Dev)
	}
	if r.Table != "" {
		args = append(args, "table", r.Table)
	}
	return ipExec(args...)
}

// writeResolvConf writes the guest's resolv.conf, unless dns names no
// nameservers.
func writeResolvConf(dns guest.DNSConfig) error {
	if len(dns.Nameservers) == 0 {
		return nil
	}

	var sb strings.Builder
	if len(dns.SearchDomains) > 0 {
		fmt.Fprintf(&sb, "search %s\n", strings.Join(dns.SearchDomains, " "))
	}
	for _, ns := range dns.Nameservers {
		fmt.Fprintf(&sb, "nameserver %s\n", ns)
	}

	if err := os.MkdirAll(overlayRoot+"/etc", 0o755); err != nil {
		return fmt.Errorf("mkdir /etc: %w", err)
	}
	if err := os.WriteFile(overlayRoot+"/etc/resolv.conf", []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}
	return nil
}

// ipExec runs an /sbin/ip sub-command, returning a descriptive error on failure.
func ipExec(args ...string) error {
	out, err := exec.Command("/sbin/ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}
