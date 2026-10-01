// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"context"
	"errors"
	"fmt"

	"github.com/konradasb/dicer/internal/types"
)

// A network's guests may reach its gateway, the host's address on the
// network. Dicer's iptables INPUT rules let them, ahead of the host's own;
// where firewalld runs, its rules come from a table iptables cannot
// override, so the bridge is also bound to firewalldZone, which decides
// what on the host they may reach, and lets their traffic be forwarded.

// ensureGatewayAccess lets a network's guests reach its gateway, and no
// other network's guests reach it. A firewalld without firewalldZone is
// logged, not returned: the guests still run, but firewalld turns their
// traffic away, to the host and beyond it.
func (h *Host) ensureGatewayAccess(ctx context.Context, nw *types.Network) error {
	h.rulesMu.Lock()
	err := ensureInputRules(ctx, nw.Bridge, nw.Gateway)
	h.rulesMu.Unlock()
	if err != nil {
		return fmt.Errorf("set up input rules: %w", err)
	}

	if h.firewalld == nil {
		return nil
	}
	err = h.firewalld.bind(ctx, nw.Bridge)
	if errors.Is(err, errNoFirewalldZone) {
		h.logger.WarnContext(ctx, "firewalld turns the network's guests away, from the host and beyond it",
			"network", nw.Name, "bridge", nw.Bridge, "error", err)
		return nil
	}
	return err
}

// removeGatewayAccess undoes ensureGatewayAccess. Failures are logged.
func (h *Host) removeGatewayAccess(ctx context.Context, nw *types.Network) {
	if h.firewalld != nil {
		if err := h.firewalld.unbind(ctx, nw.Bridge); err != nil {
			h.logger.WarnContext(ctx, "failed to unbind bridge from firewalld",
				"bridge", nw.Bridge, "error", err)
		}
	}

	h.rulesMu.Lock()
	defer h.rulesMu.Unlock()
	if err := removeInputRules(ctx, nw.Bridge); err != nil {
		h.logger.WarnContext(ctx, "failed to remove input rules", "bridge", nw.Bridge, "error", err)
	}
}
