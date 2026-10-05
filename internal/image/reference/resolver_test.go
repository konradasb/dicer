// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package reference

import (
	"context"
	"errors"
	"testing"
)

// fakeResolver returns a fixed digest or error and counts its calls.
type fakeResolver struct {
	digest string
	err    error
	calls  int
}

func (f *fakeResolver) Resolve(context.Context, *Ref) (string, error) {
	f.calls++
	return f.digest, f.err
}

func mustParse(t *testing.T, s string) *Ref {
	t.Helper()

	ref, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return ref
}

func TestResolveKeepsDigestWithoutAskingResolver(t *testing.T) {
	resolver := &fakeResolver{digest: "sha256:other"}

	resolved, err := Resolve(t.Context(), resolver, mustParse(t, "alpine@"+testDigest))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := resolved.Digest(); got != testDigest {
		t.Errorf("Digest() = %q, want %q", got, testDigest)
	}
	if resolver.calls != 0 {
		t.Errorf("the resolver was asked %d times, want none", resolver.calls)
	}
}

func TestResolveAsksResolverForTag(t *testing.T) {
	resolver := &fakeResolver{digest: "sha256:abc123def456"}
	ref := mustParse(t, "alpine:latest")

	resolved, err := Resolve(t.Context(), resolver, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := resolved.Digest(); got != "sha256:abc123def456" {
		t.Errorf("Digest() = %q, want sha256:abc123def456", got)
	}
	if got := resolved.DigestHex(); got != "abc123def456" {
		t.Errorf("DigestHex() = %q, want abc123def456", got)
	}
	if got := resolved.String(); got != ref.String() {
		t.Errorf("String() = %q, want %q", got, ref.String())
	}
}

func TestResolveWrapsResolverError(t *testing.T) {
	errUnavailable := errors.New("registry unavailable")
	resolver := &fakeResolver{err: errUnavailable}

	_, err := Resolve(t.Context(), resolver, mustParse(t, "alpine:latest"))
	if !errors.Is(err, errUnavailable) {
		t.Errorf("Resolve error = %v, want it to wrap %v", err, errUnavailable)
	}
}

func TestResolveTagWithoutResolverFails(t *testing.T) {
	if _, err := Resolve(t.Context(), nil, mustParse(t, "alpine:latest")); err == nil {
		t.Error("Resolve of a tag without a resolver succeeded")
	}
}
