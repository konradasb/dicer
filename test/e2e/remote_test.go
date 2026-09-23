// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// host is the machine under test, reached over SSH.
//
// Commands are shelled out to the ssh binary rather than driven through a Go
// SSH library: the connection details then come from the user's own SSH
// configuration -- agents, jump hosts, per-host keys -- instead of being
// reimplemented here badly.
type host struct {
	addr string
	user string
	key  string
}

// sshArgs returns the options common to every ssh and scp invocation.
//
// BatchMode fails rather than prompting, so a missing key is an error with a
// message instead of a test that hangs waiting for a passphrase.
func (h *host) sshArgs() []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
	}
	if h.key != "" {
		args = append(args, "-i", h.key)
	}

	return args
}

// target is the user@address ssh connects to.
func (h *host) target() string {
	return h.user + "@" + h.addr
}

// run executes a command on the host and returns its combined output.
//
// The arguments are quoted into one remote command line, because ssh
// concatenates whatever it is given and hands the result to a shell: passing
// them through unquoted would let an image reference or a kernel URL be
// reinterpreted as shell syntax.
func (h *host) run(ctx context.Context, argv ...string) (string, error) {
	args := append(h.sshArgs(), h.target(), quoteCommand(argv))

	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s",
			strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}

	return string(out), nil
}

// runShell executes a shell snippet on the host, for the few cases that need
// a pipeline or a redirect. Callers are responsible for what they pass.
func (h *host) runShell(ctx context.Context, script string) (string, error) {
	args := append(h.sshArgs(), h.target(), script)

	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s", script, err, strings.TrimSpace(string(out)))
	}

	return string(out), nil
}

// upload copies a local file to the host and makes it executable.
func (h *host) upload(ctx context.Context, localPath, remotePath string) error {
	args := append(h.sshArgs(), localPath, h.target()+":"+remotePath)

	if out, err := exec.CommandContext(ctx, "scp", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("upload %s: %w: %s", localPath, err, strings.TrimSpace(string(out)))
	}

	if _, err := h.run(ctx, "chmod", "+x", remotePath); err != nil {
		return fmt.Errorf("chmod %s: %w", remotePath, err)
	}

	return nil
}

// arch returns the host's machine architecture as GOARCH names it, so that a
// binary built for the wrong one is caught before it is uploaded rather than
// as "exec format error" from a VM that never boots.
func (h *host) arch(ctx context.Context) (string, error) {
	out, err := h.run(ctx, "uname", "-m")
	if err != nil {
		return "", err
	}

	switch machine := strings.TrimSpace(out); machine {
	case "x86_64":
		return "amd64", nil
	case "aarch64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported host architecture %q", machine)
	}
}

// quoteCommand renders argv as a single shell command line, quoting each
// argument so the remote shell treats it as one word.
func quoteCommand(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}

	return strings.Join(quoted, " ")
}
