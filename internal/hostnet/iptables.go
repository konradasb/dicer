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

// netAdminProcAttr passes CAP_NET_ADMIN on to iptables, which needs it.
var netAdminProcAttr = &syscall.SysProcAttr{
	AmbientCaps: []uintptr{unix.CAP_NET_ADMIN},
}

// iptablesCommand returns an iptables command, bound to ctx, that waits for
// the xtables lock.
func iptablesCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "iptables", slices.Concat([]string{"-w"}, args)...)
	cmd.SysProcAttr = netAdminProcAttr

	return cmd
}

// Dicer's chains. FORWARD only jumps to them, so their rules need no
// position in it. Their names, like the comments on Dicer's rules, are
// what Dicer finds its rules by on hosts it set up before: they must not
// change.
//
// Evaluation order in FORWARD:
//  1. DICER-USER              — admin-defined overrides (empty by default)
//  2. DICER-ISOLATION-STAGE-1 — inter-network isolation (two-stage, like Docker)
//  3. DICER-FORWARD           — per-bridge ACCEPT rules (ICC + NAT forwarding)
//
// DICER-INPUT is jumped to from INPUT, as it filters traffic to the host
// itself, such as to a gateway address, rather than through it.
const (
	chainDicerUser            = "DICER-USER"
	chainDicerIsolationStage1 = "DICER-ISOLATION-STAGE-1"
	chainDicerIsolationStage2 = "DICER-ISOLATION-STAGE-2"
	chainDicerForward         = "DICER-FORWARD"
	chainDicerInput           = "DICER-INPUT"
)

const (
	commentJumpUser      = "dicer-jump-user"
	commentJumpIsolation = "dicer-jump-isolation"
	commentJumpForward   = "dicer-jump-forward"
	commentJumpInput     = "dicer-jump-input"
)

// chainJump is a rule in a built-in parent chain, such as FORWARD or INPUT,
// tagged with comment, that jumps to one of Dicer's chains.
type chainJump struct {
	parent  string
	comment string
	target  string
}

// dicerJumps are the jumps to Dicer's filter chains. Jumps from the same
// parent chain are evaluated in slice order.
var dicerJumps = []chainJump{
	{"FORWARD", commentJumpUser, chainDicerUser},
	{"FORWARD", commentJumpIsolation, chainDicerIsolationStage1},
	{"FORWARD", commentJumpForward, chainDicerForward},
	{"INPUT", commentJumpInput, chainDicerInput},
}

// dicerChains are Dicer's chains in the filter table.
var dicerChains = []string{
	chainDicerUser,
	chainDicerIsolationStage1,
	chainDicerIsolationStage2,
	chainDicerForward,
	chainDicerInput,
}

// These return the comments that tag a bridge's rules.
func natComment(bridge string) string             { return "dicer-nat-" + bridge }
func forwardOutComment(bridge string) string      { return "dicer-fwd-out-" + bridge }
func forwardInComment(bridge string) string       { return "dicer-fwd-in-" + bridge }
func iccComment(bridge string) string             { return "dicer-icc-" + bridge }
func isolationStage1Comment(bridge string) string { return "dicer-isolation-s1-" + bridge }
func isolationStage2Comment(bridge string) string { return "dicer-isolation-s2-" + bridge }
func inputAcceptComment(bridge string) string     { return "dicer-input-accept-" + bridge }
func inputDropComment(bridge string) string       { return "dicer-input-drop-" + bridge }

// setupIPTables ensures the NAT, forwarding and isolation rules of a bridge
// and its subnet. Input rules are gateway access's: see ensureGatewayAccess.
func (h *Host) setupIPTables(ctx context.Context, bridge, subnetCIDR string) error {
	h.rulesMu.Lock()
	defer h.rulesMu.Unlock()

	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return fmt.Errorf("check ip forwarding: %w", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		return ErrForwardingDisabled
	}

	uplink, err := h.uplink()
	if err != nil {
		return err
	}

	if err := ensureDicerChains(ctx); err != nil {
		return fmt.Errorf("set up Dicer's chains: %w", err)
	}
	if err := ensureRule(ctx, "nat", "POSTROUTING", natComment(bridge),
		"-s", subnetCIDR, "-o", uplink,
		"-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("set up NAT: %w", err)
	}
	if err := ensureForwardRules(ctx, bridge, uplink); err != nil {
		return fmt.Errorf("set up forward rules: %w", err)
	}
	if err := ensureIsolationRules(ctx, bridge); err != nil {
		return fmt.Errorf("set up isolation rules: %w", err)
	}

	h.logger.InfoContext(ctx, "iptables configured",
		"bridge", bridge, "subnet", subnetCIDR, "uplink", uplink)
	return nil
}

// teardownIPTables removes the rules setupIPTables ensures for a bridge.
// Best-effort: it logs failures rather than returning them.
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

	h.logger.DebugContext(ctx, "iptables rules removed", "bridge", bridge)
}

// ensureDicerChains creates Dicer's chains and the jumps to them, putting
// the jumps from each parent chain back in order if any is missing.
func ensureDicerChains(ctx context.Context) error {
	for _, chain := range dicerChains {
		_ = iptablesCommand(ctx, "-N", chain).Run() // fails if the chain already exists, which is fine
	}

	jumpsByParent := make(map[string][]chainJump)
	for _, j := range dicerJumps {
		jumpsByParent[j.parent] = append(jumpsByParent[j.parent], j)
	}

	for parent, jumps := range jumpsByParent {
		anyMissing := slices.ContainsFunc(jumps, func(j chainJump) bool {
			return !ruleExists(ctx, "filter", parent, jumpRule(j))
		})
		if !anyMissing {
			continue
		}

		for _, j := range jumps {
			_ = deleteRulesWithComment(ctx, "filter", parent, j.comment)
		}
		// Each is inserted first, so in reverse they end up in order.
		for _, j := range slices.Backward(jumps) {
			args := slices.Concat([]string{"-I", parent, "1"}, jumpRule(j))
			if out, err := iptablesCommand(ctx, args...).CombinedOutput(); err != nil {
				return fmt.Errorf("insert %s jump to %s: %w: %s", parent, j.target, err, out)
			}
		}
	}

	return nil
}

// jumpRule returns the rule arguments of a jump.
func jumpRule(j chainJump) []string {
	return commented(j.comment, []string{"-j", j.target})
}

// ensureForwardRules adds ICC, outbound, and inbound ACCEPT rules to
// the DICER-FORWARD chain for the given bridge (idempotent).
func ensureForwardRules(ctx context.Context, bridge, uplink string) error {
	// ICC (inter-container communication): instances on the bridge reach
	// each other.
	if err := ensureRule(ctx, "filter", chainDicerForward, iccComment(bridge),
		"-i", bridge, "-o", bridge,
		"-j", "ACCEPT"); err != nil {
		return fmt.Errorf("add ICC rule for %s: %w", bridge, err)
	}

	// Outbound: bridge → uplink.
	if err := ensureRule(ctx, "filter", chainDicerForward, forwardOutComment(bridge),
		"-i", bridge, "-o", uplink,
		"-m", "conntrack", "--ctstate", "NEW,RELATED,ESTABLISHED",
		"-j", "ACCEPT"); err != nil {
		return fmt.Errorf("add outbound forward for %s: %w", bridge, err)
	}

	// Inbound: uplink → bridge (return traffic only).
	if err := ensureRule(ctx, "filter", chainDicerForward, forwardInComment(bridge),
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
		forwardOutComment(bridge),
		forwardInComment(bridge),
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
	if err := ensureRule(ctx, "filter", chainDicerIsolationStage1, isolationStage1Comment(bridge),
		"-i", bridge, "!", "-o", bridge,
		"-j", chainDicerIsolationStage2); err != nil {
		return fmt.Errorf("add isolation stage-1 rule for %s: %w", bridge, err)
	}

	if err := ensureRule(ctx, "filter", chainDicerIsolationStage2, isolationStage2Comment(bridge),
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
		{chainDicerIsolationStage1, isolationStage1Comment(bridge)},
		{chainDicerIsolationStage2, isolationStage2Comment(bridge)},
	} {
		if err := deleteRulesWithComment(ctx, "filter", pair.chain, pair.comment); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ensureInputRules accepts traffic to a bridge's gateway IP only from that
// bridge, so instances cannot reach another network's gateway. The ACCEPT and DROP
// rules are replaced together to keep their order.
func ensureInputRules(ctx context.Context, bridge, gatewayIP string) error {
	accept := commented(inputAcceptComment(bridge), []string{"-i", bridge, "-d", gatewayIP, "-j", "ACCEPT"})
	drop := commented(inputDropComment(bridge), []string{"-d", gatewayIP, "-j", "DROP"})
	if ruleExists(ctx, "filter", chainDicerInput, accept) && ruleExists(ctx, "filter", chainDicerInput, drop) {
		return nil
	}

	_ = removeInputRules(ctx, bridge)
	if err := appendRule(ctx, "filter", chainDicerInput, accept); err != nil {
		return fmt.Errorf("add input accept rule for %s: %w", bridge, err)
	}
	if err := appendRule(ctx, "filter", chainDicerInput, drop); err != nil {
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

// ensureRule appends a rule, tagged with comment, to a chain unless it is
// already there. A rule with the same comment that differs -- one naming the
// uplink the host had before -- is replaced.
func ensureRule(ctx context.Context, table, chain, comment string, args ...string) error {
	rule := commented(comment, args)
	if ruleExists(ctx, table, chain, rule) {
		return nil
	}
	_ = deleteRulesWithComment(ctx, table, chain, comment)
	return appendRule(ctx, table, chain, rule)
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

// ruleExists reports whether chain, in table, holds rule.
func ruleExists(ctx context.Context, table, chain string, rule []string) bool {
	return iptablesCommand(ctx, slices.Concat([]string{"-t", table, "-C", chain}, rule)...).Run() == nil
}

// appendRule appends rule to chain, in table.
func appendRule(ctx context.Context, table, chain string, rule []string) error {
	args := slices.Concat([]string{"-t", table, "-A", chain}, rule)
	if out, err := iptablesCommand(ctx, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("append rule to %s/%s: %w: %s", table, chain, err, out)
	}
	return nil
}

// ruleComment returns the comment of a rule as `iptables -S` prints it, or
// "" if it has none.
func ruleComment(line string) string {
	fields := strings.Fields(line)
	for i, f := range fields[:max(len(fields)-1, 0)] {
		if f == "--comment" {
			return strings.Trim(fields[i+1], `"`)
		}
	}
	return ""
}

// deleteRulesWithComment removes all rules in table/chain with the given
// comment, by specification rather than position.
func deleteRulesWithComment(ctx context.Context, table, chain, comment string) error {
	out, err := iptablesCommand(ctx, "-t", table, "-S", chain).Output()
	if err != nil {
		return fmt.Errorf("list rules in %s/%s: %w", table, chain, err)
	}

	var errs []error
	for line := range strings.SplitSeq(string(out), "\n") {
		if ruleComment(line) != comment {
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
		if out, err := iptablesCommand(ctx, slices.Concat([]string{"-t", table}, args)...).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("delete rule %q in %s/%s: %w: %s", line, table, chain, err, out))
		}
	}

	return errors.Join(errs...)
}
