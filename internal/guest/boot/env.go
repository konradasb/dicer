// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"fmt"
	"maps"
	"strings"
)

// guestHome is the HOME the workload runs with unless its environment sets
// one.
const guestHome = "/root"

// guestEnv returns env as the KEY=VALUE pairs exec.Cmd.Env takes, with PATH
// and HOME defaulted if env does not set them.
func guestEnv(env map[string]string) []string {
	out := make([]string, 0, len(env)+2)
	for k, v := range envWithDefaults(env) {
		out = append(out, k+"="+v)
	}
	return out
}

// envFileContents returns env as the contents of a systemd EnvironmentFile,
// with PATH and HOME defaulted if env does not set them. Values are
// double-quoted, with backslashes and quotes escaped.
func envFileContents(env map[string]string) string {
	var b strings.Builder
	for k, v := range envWithDefaults(env) {
		fmt.Fprintf(&b, "%s=%s\n", k, doubleQuote(v))
	}
	return b.String()
}

// envWithDefaults returns a copy of env with PATH and HOME set to the
// guest's defaults where env leaves them unset.
func envWithDefaults(env map[string]string) map[string]string {
	out := maps.Clone(env)
	if out == nil {
		out = make(map[string]string, 2)
	}
	if _, ok := out["PATH"]; !ok {
		out["PATH"] = guestPath
	}
	if _, ok := out["HOME"]; !ok {
		out["HOME"] = guestHome
	}
	return out
}

// doubleQuote wraps s in double quotes, escaping backslashes and double quotes.
func doubleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
