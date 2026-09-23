// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"fmt"
	"strings"
)

// guestEnv converts an environment map to the "KEY=VALUE" slice expected by
// exec.Cmd.Env. PATH and HOME are added with safe defaults if absent.
func guestEnv(env map[string]string) []string {
	out := make([]string, 0, len(env)+2)
	for k, v := range env {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	if _, ok := env["PATH"]; !ok {
		out = append(out, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	if _, ok := env["HOME"]; !ok {
		out = append(out, "HOME=/root")
	}

	return out
}

// buildEnvFile returns a systemd EnvironmentFile-compatible string for env.
// Values are double-quoted with backslashes and quotes escaped.
// PATH and HOME are appended with safe defaults if absent.
func buildEnvFile(env map[string]string) string {
	var b strings.Builder
	for k, v := range env {
		fmt.Fprintf(&b, "%s=%s\n", k, doubleQuote(v))
	}
	if _, ok := env["PATH"]; !ok {
		b.WriteString("PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n")
	}
	if _, ok := env["HOME"]; !ok {
		b.WriteString("HOME=/root\n")
	}

	return b.String()
}

// doubleQuote wraps s in double quotes, escaping backslashes and double quotes.
func doubleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
