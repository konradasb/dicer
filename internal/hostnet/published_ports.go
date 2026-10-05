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

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// chainDicerDNAT, in the nat table, holds the DNAT rules of every published
// port. It is jumped to from PREROUTING and OUTPUT.
const chainDicerDNAT = "DICER-DNAT"

const (
	commentJumpDNATPrerouting = "dicer-jump-dnat-prerouting"
	commentJumpDNATOutput     = "dicer-jump-dnat-output"
)

// dnatJumps are the jumps to DICER-DNAT, for traffic addressed to the host.
// Loopback is excluded, as it would need route_localnet.
var dnatJumps = []struct {
	parent  string
	comment string
	match   []string
}{
	{"PREROUTING", commentJumpDNATPrerouting, []string{"-m", "addrtype", "--dst-type", "LOCAL"}},
	{"OUTPUT", commentJumpDNATOutput, []string{"!", "-d", "127.0.0.0/8", "-m", "addrtype", "--dst-type", "LOCAL"}},
}

// portComment returns the comment that tags every rule publishing one of
// an instance's ports.
func portComment(instanceID string) string { return "dicer-port-" + instanceID }

// PublishPorts forwards each host port to the instance's address, replacing
// the instance's existing rules. A host port already in use is refused.
func (h *Host) PublishPorts(
	ctx context.Context, nw *types.Network, allocation *types.NetworkAllocation, ports []types.PortMapping,
) error {
	h.rulesMu.Lock()
	defer h.rulesMu.Unlock()

	if err := ensureDNATChain(ctx); err != nil {
		return fmt.Errorf("set up %s chain: %w", chainDicerDNAT, err)
	}

	h.unpublishPorts(ctx, allocation.InstanceID)

	for _, p := range ports {
		if err := checkHostPortFree(ctx, p); err != nil {
			return err
		}
	}

	comment := portComment(allocation.InstanceID)
	for _, p := range ports {
		protocol, hostPort, guestPort := p.EffectiveProtocol(), strconv.Itoa(int(p.HostPort)), strconv.Itoa(int(p.GuestPort))

		dnat := []string{"-p", protocol}
		if p.HostIP != "" {
			dnat = append(dnat, "-d", p.HostIP)
		}
		dnat = append(dnat, "--dport", hostPort,
			"-m", "comment", "--comment", comment,
			"-j", "DNAT", "--to-destination", net.JoinHostPort(allocation.IP, guestPort))

		// Admit only connections this DNAT redirected.
		forward := []string{
			"-d", allocation.IP, "-o", nw.Bridge, "-p", protocol, "--dport", guestPort,
			"-m", "conntrack", "--ctstate", "DNAT",
			"-m", "comment", "--comment", comment,
			"-j", "ACCEPT",
		}

		err := appendRule(ctx, "filter", chainDicerForward, forward)
		if err == nil {
			err = appendRule(ctx, "nat", chainDicerDNAT, dnat)
		}
		if err != nil {
			h.unpublishPorts(context.WithoutCancel(ctx), allocation.InstanceID)
			return fmt.Errorf("publish port %s: %w", p, err)
		}
	}

	if len(ports) > 0 {
		h.logger.InfoContext(ctx, "published ports",
			"instance_id", allocation.InstanceID, "ip", allocation.IP, "ports", ports)
	}
	return nil
}

// UnpublishPorts removes every port the instance has published. Best-effort:
// it logs failures rather than returning them.
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
	_ = iptablesCommand(ctx, "-t", "nat", "-N", chainDicerDNAT).Run() // fails if it exists, which is fine

	for _, j := range dnatJumps {
		rule := slices.Concat(j.match,
			[]string{"-m", "comment", "--comment", j.comment, "-j", chainDicerDNAT})

		if ruleExists(ctx, "nat", j.parent, rule) {
			continue
		}

		insert := iptablesCommand(ctx, slices.Concat([]string{"-t", "nat", "-I", j.parent, "1"}, rule)...)
		if out, err := insert.CombinedOutput(); err != nil {
			return fmt.Errorf("insert %s jump to %s: %w: %s", j.parent, chainDicerDNAT, err, out)
		}
	}

	return nil
}

// chainExists reports whether the chain exists in the table.
func chainExists(ctx context.Context, table, chain string) bool {
	return iptablesCommand(ctx, "-t", table, "-S", chain).Run() == nil
}

// checkHostPortFree refuses a host port a process is listening on, by
// briefly listening on it.
func checkHostPortFree(ctx context.Context, p types.PortMapping) error {
	addr := net.JoinHostPort(p.HostIP, strconv.Itoa(int(p.HostPort)))

	var (
		lc  net.ListenConfig
		err error
	)
	switch p.EffectiveProtocol() {
	case types.ProtocolUDP:
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
		return errdefs.InvalidArgument("cannot publish port %s: %s is not an address of this host",
			p, p.HostIP)
	default:
		return errdefs.InvalidState("cannot publish port %s: something on the host is already using it (%w)", p, err)
	}
}
