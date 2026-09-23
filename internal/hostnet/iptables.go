// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// netAdminProcAttr propagates CAP_NET_ADMIN to child processes.
// Required for iptables and ip commands.
var netAdminProcAttr = &syscall.SysProcAttr{
	AmbientCaps: []uintptr{unix.CAP_NET_ADMIN},
}

// iptablesCmd builds an iptables invocation carrying the capability the child
// needs, bound to ctx so that a hung call cannot block teardown indefinitely.
// It waits for the xtables lock rather than failing when another program,
// such as Docker, holds it.
func iptablesCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "iptables", slices.Concat([]string{"-w"}, args)...)
	cmd.SysProcAttr = netAdminProcAttr

	return cmd
}

// Custom chain names. The FORWARD chain only contains jumps to these chains,
// keeping it clean and avoiding position-dependent insertion issues.
//
// Evaluation order in FORWARD:
//  1. DICER-USER              — admin-defined overrides (empty by default)
//  2. DICER-ISOLATION-STAGE-1 — inter-network isolation (two-stage, like Docker)
//  3. DICER-FORWARD           — per-bridge ACCEPT rules (ICC + NAT forwarding)
//
// The INPUT chain jump is separate from FORWARD because it applies to traffic
// destined for the host (e.g. gateway IP), not traffic being forwarded through it.
const (
	chainDicerUser       = "DICER-USER"
	chainIsolationStage1 = "DICER-ISOLATION-STAGE-1"
	chainIsolationStage2 = "DICER-ISOLATION-STAGE-2"
	chainDicerForward    = "DICER-FORWARD"
	chainDicerInput      = "DICER-INPUT"
)

const (
	commentJumpUser      = "dicer-jump-user"
	commentJumpIsolation = "dicer-jump-isolation"
	commentJumpForward   = "dicer-jump-forward"
	commentJumpInput     = "dicer-jump-input"
)

// chainJump maps a comment tag to its target chain and the parent chain it
// is inserted into (e.g. FORWARD or INPUT).
type chainJump struct {
	parent  string
	comment string
	target  string
}

// dicerJumps defines all built-in chain jumps grouped by parent chain.
// Within the same parent chain the evaluation order matches slice order.
var dicerJumps = []chainJump{
	{"FORWARD", commentJumpUser, chainDicerUser},
	{"FORWARD", commentJumpIsolation, chainIsolationStage1},
	{"FORWARD", commentJumpForward, chainDicerForward},
	{"INPUT", commentJumpInput, chainDicerInput},
}

// dicerChains lists all custom chains we manage.
var dicerChains = []string{
	chainDicerUser,
	chainIsolationStage1,
	chainIsolationStage2,
	chainDicerForward,
	chainDicerInput,
}

// Per-bridge comment helpers.
func natComment(bridge string) string         { return "dicer-nat-" + bridge }
func fwdOutComment(bridge string) string      { return "dicer-fwd-out-" + bridge }
func fwdInComment(bridge string) string       { return "dicer-fwd-in-" + bridge }
func iccComment(bridge string) string         { return "dicer-icc-" + bridge }
func isolationS1Comment(bridge string) string { return "dicer-isolation-s1-" + bridge }
func isolationS2Comment(bridge string) string { return "dicer-isolation-s2-" + bridge }
func inputAcceptComment(bridge string) string { return "dicer-input-accept-" + bridge }
func inputDropComment(bridge string) string   { return "dicer-input-drop-" + bridge }

// setupIPTables ensures NAT, forwarding, isolation, and input rules are in place for the given bridge and subnet.
func (h *Host) setupIPTables(ctx context.Context, bridge, subnetCIDR, gatewayIP string) error {
	h.rulesMu.Lock()
	defer h.rulesMu.Unlock()

	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return fmt.Errorf("check ip forwarding: %w", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		return ErrForwardingDisabled
	}

	uplink, err := h.resolveUplink()
	if err != nil {
		return err
	}
	h.logger.InfoContext(ctx, "uplink detected", "interface", uplink)

	if err := ensureDicerChains(ctx); err != nil {
		return fmt.Errorf("setup dicer chains: %w", err)
	}
	if err := ensureNATRule(ctx, subnetCIDR, uplink, natComment(bridge)); err != nil {
		return fmt.Errorf("setup NAT: %w", err)
	}
	if err := ensureForwardRules(ctx, bridge, uplink); err != nil {
		return fmt.Errorf("setup forward rules: %w", err)
	}
	if err := ensureIsolationRules(ctx, bridge); err != nil {
		return fmt.Errorf("setup isolation rules: %w", err)
	}
	if err := ensureInputRules(ctx, bridge, gatewayIP); err != nil {
		return fmt.Errorf("setup input rules: %w", err)
	}

	h.logger.InfoContext(ctx, "iptables configured",
		"subnet", subnetCIDR, "uplink", uplink)
	return nil
}

// teardownIPTables removes all iptables rules for a specific bridge/subnet.
// Best-effort: logs failures but does not abort on individual rule removal errors.
func (h *Host) teardownIPTables(ctx context.Context, bridge string) {
	h.rulesMu.Lock()
	defer h.rulesMu.Unlock()

	if err := deleteRulesWithComment(ctx, "nat", "POSTROUTING", natComment(bridge)); err != nil {
		h.logger.WarnContext(ctx, "failed to remove NAT rule", "bridge", bridge, "error", err)
	}
	if err := removeForwardRules(ctx, bridge); err != nil {
		h.logger.WarnContext(ctx, "failed to remove forward rules", "bridge", bridge, "error", err)
	}
	if err := removeIsolationRules(ctx, bridge); err != nil {
		h.logger.WarnContext(ctx, "failed to remove isolation rules", "bridge", bridge, "error", err)
	}
	if err := removeInputRules(ctx, bridge); err != nil {
		h.logger.WarnContext(ctx, "failed to remove input rules", "bridge", bridge, "error", err)
	}

	h.logger.DebugContext(ctx, "iptables rules removed",
		"bridge", bridge)
}

// ensureDicerChains creates all custom chains and inserts FORWARD jumps in the
// correct evaluation order (idempotent).
func ensureDicerChains(ctx context.Context) error {
	for _, chain := range dicerChains {
		cmd := iptablesCmd(ctx, "-N", chain)
		_ = cmd.Run() // fails if the chain already exists, which is fine
	}

	// Group jumps by parent chain, preserving slice order within each group.
	groups := make(map[string][]chainJump)
	for _, j := range dicerJumps {
		groups[j.parent] = append(groups[j.parent], j)
	}

	for parent, jumps := range groups {
		// Check if all jumps for this parent are present.
		allPresent := true
		for _, j := range jumps {
			check := iptablesCmd(ctx, "-C", parent,
				"-m", "comment", "--comment", j.comment,
				"-j", j.target)
			if check.Run() != nil {
				allPresent = false
				break
			}
		}
		if allPresent {
			continue
		}

		// Remove stale jumps and re-insert in correct order.
		for _, j := range jumps {
			_ = deleteRulesWithComment(ctx, "filter", parent, j.comment)
		}
		// Insert in reverse so they end up in the correct order at position 1.
		for _, j := range slices.Backward(jumps) {
			cmd := iptablesCmd(ctx, "-I", parent, "1",
				"-m", "comment", "--comment", j.comment,
				"-j", j.target)
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("insert %s jump to %s: %w", parent, j.target, err)
			}
		}
	}

	return nil
}

// ensureNATRule adds a MASQUERADE rule for the given subnet if not present.
func ensureNATRule(ctx context.Context, subnetCIDR, uplink, comment string) error {
	check := iptablesCmd(ctx, "-t", "nat", "-C", "POSTROUTING",
		"-s", subnetCIDR, "-o", uplink,
		"-m", "comment", "--comment", comment,
		"-j", "MASQUERADE")
	if check.Run() == nil {
		return nil // already present
	}

	// Remove any stale rule with our comment (handles uplink renames).
	_ = deleteRulesWithComment(ctx, "nat", "POSTROUTING", comment)

	add := iptablesCmd(ctx, "-t", "nat", "-A", "POSTROUTING",
		"-s", subnetCIDR, "-o", uplink,
		"-m", "comment", "--comment", comment,
		"-j", "MASQUERADE")
	if err := add.Run(); err != nil {
		return fmt.Errorf("add MASQUERADE: %w", err)
	}
	return nil
}

// ensureForwardRules adds ICC, outbound, and inbound ACCEPT rules to
// the DICER-FORWARD chain for the given bridge (idempotent).
func ensureForwardRules(ctx context.Context, bridge, uplink string) error {
	// ICC: allow traffic within the same bridge (inter-container communication).
	if err := appendChainRule(ctx, chainDicerForward, iccComment(bridge),
		"-i", bridge, "-o", bridge,
		"-j", "ACCEPT"); err != nil {
		return fmt.Errorf("add ICC rule for %s: %w", bridge, err)
	}

	// Outbound: bridge → uplink.
	if err := appendChainRule(ctx, chainDicerForward, fwdOutComment(bridge),
		"-i", bridge, "-o", uplink,
		"-m", "conntrack", "--ctstate", "NEW,RELATED,ESTABLISHED",
		"-j", "ACCEPT"); err != nil {
		return fmt.Errorf("add outbound forward for %s: %w", bridge, err)
	}

	// Inbound: uplink → bridge (return traffic only).
	if err := appendChainRule(ctx, chainDicerForward, fwdInComment(bridge),
		"-i", uplink, "-o", bridge,
		"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED",
		"-j", "ACCEPT"); err != nil {
		return fmt.Errorf("add inbound forward for %s: %w", bridge, err)
	}

	return nil
}

// removeForwardRules removes all per-bridge rules from DICER-FORWARD.
func removeForwardRules(ctx context.Context, bridge string) error {
	var errs []error
	for _, comment := range []string{
		iccComment(bridge),
		fwdOutComment(bridge),
		fwdInComment(bridge),
	} {
		if err := deleteRulesWithComment(ctx, "filter", chainDicerForward, comment); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ensureIsolationRules adds per-bridge rules to the two-stage isolation chains.
// Stage 1: traffic leaving this bridge (not staying on it) → jump to stage 2.
// Stage 2: traffic destined for this bridge → DROP.
func ensureIsolationRules(ctx context.Context, bridge string) error {
	if err := appendChainRule(ctx, chainIsolationStage1, isolationS1Comment(bridge),
		"-i", bridge, "!", "-o", bridge,
		"-j", chainIsolationStage2); err != nil {
		return fmt.Errorf("add isolation stage-1 rule for %s: %w", bridge, err)
	}

	if err := appendChainRule(ctx, chainIsolationStage2, isolationS2Comment(bridge),
		"-o", bridge,
		"-j", "DROP"); err != nil {
		return fmt.Errorf("add isolation stage-2 rule for %s: %w", bridge, err)
	}

	return nil
}

// removeIsolationRules removes per-bridge rules from both isolation chains.
func removeIsolationRules(ctx context.Context, bridge string) error {
	var errs []error
	for _, pair := range []struct{ chain, comment string }{
		{chainIsolationStage1, isolationS1Comment(bridge)},
		{chainIsolationStage2, isolationS2Comment(bridge)},
	} {
		if err := deleteRulesWithComment(ctx, "filter", pair.chain, pair.comment); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ensureInputRules restricts access to a bridge's gateway IP so that only
// traffic arriving on the bridge itself is accepted; all other traffic to
// the gateway IP is dropped. This prevents VMs on one network from reaching
// the gateway (host) IP of another network.
//
// The ACCEPT must come before the DROP, so unless both are in place they are
// replaced together, in that order. Because each pair matches a unique
// destination IP, ordering between different bridges does not matter.
func ensureInputRules(ctx context.Context, bridge, gatewayIP string) error {
	accept := commented(inputAcceptComment(bridge), []string{"-i", bridge, "-d", gatewayIP, "-j", "ACCEPT"})
	drop := commented(inputDropComment(bridge), []string{"-d", gatewayIP, "-j", "DROP"})
	if ruleExists(ctx, chainDicerInput, accept) && ruleExists(ctx, chainDicerInput, drop) {
		return nil
	}

	_ = removeInputRules(ctx, bridge)
	if err := appendRule(ctx, chainDicerInput, accept); err != nil {
		return fmt.Errorf("add input accept rule for %s: %w", bridge, err)
	}
	if err := appendRule(ctx, chainDicerInput, drop); err != nil {
		return fmt.Errorf("add input drop rule for %s: %w", bridge, err)
	}
	return nil
}

// removeInputRules removes per-bridge rules from the DICER-INPUT chain.
func removeInputRules(ctx context.Context, bridge string) error {
	var errs []error
	for _, comment := range []string{
		inputAcceptComment(bridge),
		inputDropComment(bridge),
	} {
		if err := deleteRulesWithComment(ctx, "filter", chainDicerInput, comment); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// appendChainRule appends a rule to the given chain unless it is already
// there (idempotent). A rule with the same comment that differs -- one naming
// the uplink the host had before -- is replaced.
func appendChainRule(ctx context.Context, chain, comment string, args ...string) error {
	rule := commented(comment, args)
	if ruleExists(ctx, chain, rule) {
		return nil
	}
	_ = deleteRulesWithComment(ctx, "filter", chain, comment)
	return appendRule(ctx, chain, rule)
}

// commented returns the rule args with a match on comment before its
// terminal -j target.
func commented(comment string, args []string) []string {
	rule := slices.Clone(args)
	if i := slices.Index(rule, "-j"); i >= 0 {
		rule = slices.Insert(rule, i, "-m", "comment", "--comment", comment)
	}
	return rule
}

// ruleExists reports whether chain holds rule, in the filter table.
func ruleExists(ctx context.Context, chain string, rule []string) bool {
	return iptablesCmd(ctx, slices.Concat([]string{"-C", chain}, rule)...).Run() == nil
}

// appendRule appends rule to chain, in the filter table.
func appendRule(ctx context.Context, chain string, rule []string) error {
	if out, err := iptablesCmd(ctx, slices.Concat([]string{"-A", chain}, rule)...).CombinedOutput(); err != nil {
		return fmt.Errorf("append rule to %s: %w: %s", chain, err, out)
	}
	return nil
}

// specComment returns the comment of a rule as `iptables -S` prints it,
// "-A CHAIN ... -m comment --comment NAME -j TARGET", or "" if it has none.
// A comment is compared whole: one bridge's name may be a prefix of
// another's, and so may the comments naming them.
func specComment(line string) string {
	fields := strings.Fields(line)
	for i, f := range fields[:max(len(fields)-1, 0)] {
		if f == "--comment" {
			return strings.Trim(fields[i+1], `"`)
		}
	}
	return ""
}

// deleteRulesWithComment removes all rules in table/chain whose comment
// matches. Each is deleted by its specification rather than its position, so
// a rule another program inserts meanwhile cannot shift the wrong one under
// the delete.
func deleteRulesWithComment(ctx context.Context, table, chain, comment string) error {
	out, err := iptablesCmd(ctx, "-t", table, "-S", chain).Output()
	if err != nil {
		return err
	}

	var errs []error
	for line := range strings.SplitSeq(string(out), "\n") {
		if specComment(line) != comment {
			continue
		}
		// "-A CHAIN ..." is deleted as "-D CHAIN ...". The rules carrying
		// a comment are this package's, and none has an argument with a
		// space in it, so the fields are the arguments once unquoted.
		args := strings.Fields(line)
		args[0] = "-D"
		for i, a := range args {
			args[i] = strings.Trim(a, `"`)
		}
		if out, err := iptablesCmd(ctx, slices.Concat([]string{"-t", table}, args)...).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("delete rule %q in %s/%s: %w: %s", line, table, chain, err, out))
		}
	}

	return errors.Join(errs...)
}
