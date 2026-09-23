// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestRemoteClientEnrolsAndIsRevoked drives remote access end to end
// against the real daemon: a token from its socket, a client enrolling over
// mutual TLS with it, that client using the API, and losing it once removed.
//
// The client runs on the host too, with a configuration directory of its
// own, so it holds a key the daemon has never seen. What it proves is the
// daemon's TLS listener and trust store, which no unit test runs for real.
func TestRemoteClientEnrolsAndIsRevoked(t *testing.T) {
	const clientName = "e2e-client"

	// The fingerprint the daemon reports is what clients pin, and what an
	// operator checks a client against.
	if out := env.dicer(t, "info"); !strings.Contains(out, "sha256:") {
		t.Errorf("info = %q, want the daemon's fingerprint", out)
	}

	out := env.dicer(t, "token", "create", clientName, "--ttl", "5m")
	token := tokenFrom(t, out)

	remoteDicer := func(args ...string) (string, error) {
		argv := append([]string{"env", "DICER_CONFIG_DIR=" + env.paths.clientConfig, env.paths.dicer}, args...)
		return env.host.run(t.Context(), argv...)
	}
	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()
		_, _ = env.host.run(ctx, "rm", "-rf", env.paths.clientConfig)
		_, _ = env.runDicer(ctx, "client", "delete", clientName)
	})

	if out, err := remoteDicer("remote", "create", "e2e", token); err != nil {
		t.Fatalf("remote create: %v\n%s", err, out)
	}

	out, err := remoteDicer("--remote", "e2e", "info")
	if err != nil {
		t.Fatalf("info over TLS: %v\n%s", err, out)
	}
	if !strings.Contains(out, "tcp://"+env.paths.api) {
		t.Errorf("info = %q, want it to name the TCP remote", out)
	}

	env.dicer(t, "client", "delete", clientName)

	if out, err := remoteDicer("--remote", "e2e", "info"); err == nil {
		t.Errorf("a removed client was still served:\n%s", out)
	}
}

// tokenFrom picks the enrolment token out of 'dicer token create' output.
func tokenFrom(t *testing.T, out string) string {
	t.Helper()

	for field := range strings.FieldsSeq(out) {
		if strings.HasPrefix(field, "dicer1.") {
			return field
		}
	}

	t.Fatalf("no token in:\n%s", out)
	return ""
}
