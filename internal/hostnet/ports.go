// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"syscall"

	"github.com/dicer-sh/dicer"
)

// chainDicerDNAT holds the DNAT rules of every published port, in the nat
// table. It is reached from PREROUTING, for traffic arriving from elsewhere,
// and from OUTPUT, for connections the host itself makes to one of its own
// addresses.
const chainDicerDNAT = "DICER-DNAT"

const (
	commentJumpDNATPrerouting = "dicer-jump-dnat-prerouting"
	commentJumpDNATOutput     = "dicer-jump-dnat-output"
)

// dnatJumps are the jumps into DICER-DNAT. Only traffic addressed to the
// host is considered, so a published port never captures traffic the host
// merely forwards. Loopback is excluded from OUTPUT: forwarding 127.0.0.1 to
// a guest would need route_localnet on the bridges, which lets guests reach
// the host's loopback-only services unless carefully fenced off.
var dnatJumps = []struct {
	parent  string
	comment string
	match   []string
}{
	{"PREROUTING", commentJumpDNATPrerouting, []string{"-m", "addrtype", "--dst-type", "LOCAL"}},
	{"OUTPUT", commentJumpDNATOutput, []string{"!", "-d", "127.0.0.0/8", "-m", "addrtype", "--dst-type", "LOCAL"}},
}

// portComment tags every rule publishing a port of the instance. It is
// derived from the instance ID alone, so the rules can be found and removed
// without knowing what was published -- after a crash, or once the
// definition has changed.
func portComment(instanceID string) string { return "dicer-port-" + instanceID }

// PublishPorts forwards each host port to the instance's address on the
// network. Rules the instance already has are replaced, so calling it again
// is harmless.
//
// A host port something on the host is already listening on is refused:
// the DNAT rule would silently steal its traffic.
func (h *Host) PublishPorts(
	ctx context.Context, nw *dicer.Network, alloc *dicer.NetworkAllocation, ports []dicer.PortMapping,
) error {
	h.rulesMu.Lock()
	defer h.rulesMu.Unlock()

	if err := ensureDNATChain(ctx); err != nil {
		return fmt.Errorf("set up %s chain: %w", chainDicerDNAT, err)
	}

	h.unpublishPorts(ctx, alloc.InstanceID)

	for _, p := range ports {
		if err := checkHostPortFree(ctx, p); err != nil {
			return err
		}
	}

	comment := portComment(alloc.InstanceID)
	for _, p := range ports {
		proto, hostPort, guestPort := p.Proto(), strconv.Itoa(int(p.HostPort)), strconv.Itoa(int(p.GuestPort))

		dnat := []string{"-t", "nat", "-A", chainDicerDNAT, "-p", proto}
		if p.HostIP != "" {
			dnat = append(dnat, "-d", p.HostIP)
		}
		dnat = append(dnat, "--dport", hostPort,
			"-m", "comment", "--comment", comment,
			"-j", "DNAT", "--to-destination", net.JoinHostPort(alloc.IP, guestPort))

		// The DNATed connection is forwarded to the bridge like any other,
		// and DICER-FORWARD only lets established traffic in from outside.
		// Matching on the DNAT state admits exactly what a published port
		// redirected, not everything aimed at the guest's port.
		forward := []string{"-A", chainDicerForward,
			"-d", alloc.IP, "-o", nw.Bridge, "-p", proto, "--dport", guestPort,
			"-m", "conntrack", "--ctstate", "DNAT",
			"-m", "comment", "--comment", comment,
			"-j", "ACCEPT"}

		for _, args := range [][]string{forward, dnat} {
			if out, err := iptablesCmd(ctx, args...).CombinedOutput(); err != nil {
				h.unpublishPorts(context.WithoutCancel(ctx), alloc.InstanceID)
				return fmt.Errorf("publish port %s: %w: %s", p, err, out)
			}
		}
	}

	if len(ports) > 0 {
		h.logger.InfoContext(ctx, "published ports",
			"instance_id", alloc.InstanceID, "ip", alloc.IP, "ports", ports)
	}
	return nil
}

// UnpublishPorts removes every port the instance has published. Best-effort:
// logs failures but does not return an error.
func (h *Host) UnpublishPorts(ctx context.Context, instanceID string) {
	h.rulesMu.Lock()
	defer h.rulesMu.Unlock()

	h.unpublishPorts(ctx, instanceID)
}

// unpublishPorts is UnpublishPorts for a caller holding rulesMu.
func (h *Host) unpublishPorts(ctx context.Context, instanceID string) {
	comment := portComment(instanceID)

	for _, c := range []struct{ table, chain string }{
		{"nat", chainDicerDNAT},
		{"filter", chainDicerForward},
	} {
		// Nothing was ever published on a host without the chain; listing
		// it would only fail.
		if !chainExists(ctx, c.table, c.chain) {
			continue
		}
		if err := deleteRulesWithComment(ctx, c.table, c.chain, comment); err != nil {
			h.logger.WarnContext(ctx, "failed to remove published ports",
				"instance_id", instanceID, "table", c.table, "chain", c.chain, "error", err)
		}
	}
}

// ensureDNATChain creates DICER-DNAT and the jumps into it (idempotent).
func ensureDNATChain(ctx context.Context) error {
	_ = iptablesCmd(ctx, "-t", "nat", "-N", chainDicerDNAT).Run() // fails if it exists, which is fine

	for _, j := range dnatJumps {
		rule := slices.Concat(j.match,
			[]string{"-m", "comment", "--comment", j.comment, "-j", chainDicerDNAT})

		check := iptablesCmd(ctx, slices.Concat([]string{"-t", "nat", "-C", j.parent}, rule)...)
		if check.Run() == nil {
			continue
		}

		insert := iptablesCmd(ctx, slices.Concat([]string{"-t", "nat", "-I", j.parent, "1"}, rule)...)
		if out, err := insert.CombinedOutput(); err != nil {
			return fmt.Errorf("insert %s jump to %s: %w: %s", j.parent, chainDicerDNAT, err, out)
		}
	}

	return nil
}

// chainExists reports whether the chain exists in the table.
func chainExists(ctx context.Context, table, chain string) bool {
	return iptablesCmd(ctx, "-t", table, "-S", chain).Run() == nil
}

// checkHostPortFree refuses a host port that a process on the host is
// listening on, by trying to listen on it briefly. It cannot see ports
// published for other instances, which hold no socket; the caller checks
// those against the definitions.
func checkHostPortFree(ctx context.Context, p dicer.PortMapping) error {
	addr := net.JoinHostPort(p.HostIP, strconv.Itoa(int(p.HostPort)))

	var (
		lc  net.ListenConfig
		err error
	)
	switch p.Proto() {
	case dicer.ProtocolUDP:
		var conn net.PacketConn
		if conn, err = lc.ListenPacket(ctx, "udp4", addr); err == nil {
			_ = conn.Close()
		}
	default:
		var l net.Listener
		if l, err = lc.Listen(ctx, "tcp4", addr); err == nil {
			_ = l.Close()
		}
	}

	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EACCES):
		// A daemon without CAP_NET_BIND_SERVICE cannot bind a low port to
		// find out; that says nothing about whether it is in use.
		return nil
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return dicer.InvalidArgument("cannot publish port %s: %s is not an address of this host",
			p, p.HostIP)
	default:
		return dicer.InvalidState("cannot publish port %s: something on the host is already using it (%w)", p, err)
	}
}
