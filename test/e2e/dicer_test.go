// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

// This file is the generic CLI driver: running a dicer command on the host,
// bounding it, and decoding what it prints. Anything specific to one resource
// -- how to create an instance, how to read a volume back -- lives beside
// that resource's tests.

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// commandTimeout bounds a single CLI call. Starting an instance pulls an
// image and boots a guest, so it is the slowest of them by far.
const commandTimeout = 5 * time.Minute

// dicer runs a dicer command on the host and returns its output, failing the
// test if it does not succeed.
//
// The tests drive the CLI rather than the gRPC API on purpose: it is the
// interface a user has, so a change that breaks it should break a test, and
// the path exercised is the whole stack rather than everything below the last
// layer.
func (e *environment) dicer(t *testing.T, args ...string) string {
	t.Helper()

	out, err := e.tryDicer(t, args...)
	if err != nil {
		t.Fatalf("dicer %s: %v", strings.Join(args, " "), err)
	}

	return out
}

// tryDicer runs a dicer command and returns the error rather than failing, so
// callers can poll or assert on a failure.
func (e *environment) tryDicer(t *testing.T, args ...string) (string, error) {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	return e.runDicer(ctx, args...)
}

// commandContext bounds one command by the test's own context.
func commandContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithTimeout(t.Context(), commandTimeout)
}

// cleanupContext bounds a command run from t.Cleanup.
//
// Cleanup cannot use the test's context: that one is cancelled just before
// cleanup functions run, so every command would abort immediately and leave
// the resource it was meant to remove behind on the host.
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), commandTimeout)
}

// runDicer runs a dicer command against the daemon under test.
func (e *environment) runDicer(ctx context.Context, args ...string) (string, error) {
	argv := append([]string{e.paths.dicer, "--remote", e.remote()}, args...)

	return e.host.run(ctx, argv...)
}

// rows decodes the JSON output of a list or show command.
func rows[T any](t *testing.T, out, what string) []T {
	t.Helper()

	var decoded []T
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode %s: %v\noutput:\n%s", what, err, out)
	}

	return decoded
}

// isNotFound reports whether a command failed because the resource is
// already gone, which is the normal case when a test deleted it itself.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "code = NotFound")
}

// journal returns the daemon's log, for reporting when a test fails.
func (e *environment) journal(ctx context.Context) string {
	out, err := e.host.run(ctx, "journalctl", "-u", e.paths.unit, "--no-pager", "-n", "200")
	if err != nil {
		return fmt.Sprintf("(could not read the daemon log: %v)", err)
	}

	return out
}
