// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package reference

import (
	"context"
	"errors"
	"testing"
)

type mockResolver struct {
	digest string
	err    error
}

func (m *mockResolver) Resolve(ctx context.Context, ref *Ref) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.digest, nil
}

func TestResolve_WithDigest(t *testing.T) {
	ctx := context.Background()
	resolver := &mockResolver{
		digest: "sha256:resolved123",
		err:    nil,
	}

	// When ref already has digest, resolver should not be called
	resolved, err := Resolve(ctx, resolver, "alpine@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.ManifestDigest() != "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef" {
		t.Errorf("ManifestDigest() = %v, want sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", resolved.ManifestDigest())
	}

	// Verify resolver wasn't called by checking it returns original digest
	if resolved.ManifestDigest() == resolver.digest {
		t.Error("resolver should not be called when ref has digest")
	}
}

func TestResolve_WithoutDigest(t *testing.T) {
	ctx := context.Background()
	resolver := &mockResolver{
		digest: "sha256:resolved123",
		err:    nil,
	}

	resolved, err := Resolve(ctx, resolver, "alpine:latest")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.ManifestDigest() != "sha256:resolved123" {
		t.Errorf("ManifestDigest() = %v, want sha256:resolved123", resolved.ManifestDigest())
	}

	if resolved.String() == "" {
		t.Error("String() should return normalized ref")
	}
}

func TestResolve_InvalidReference(t *testing.T) {
	ctx := context.Background()
	resolver := &mockResolver{}

	_, err := Resolve(ctx, resolver, "INVALID::**")
	if err == nil {
		t.Error("Resolve() should fail with invalid reference")
	}
}

func TestResolve_ResolverError(t *testing.T) {
	ctx := context.Background()
	testErr := errors.New("registry unavailable")
	resolver := &mockResolver{
		err: testErr,
	}

	_, err := Resolve(ctx, resolver, "alpine:latest")
	if err == nil {
		t.Error("Resolve() should return resolver error")
	}
	if !errors.Is(err, testErr) {
		t.Errorf("error should wrap resolver error")
	}
}

func TestNewResolvedRef(t *testing.T) {
	ref, err := Parse("alpine:latest")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	digest := "sha256:abc123"
	resolved := NewResolvedRef(ref, digest)

	if resolved.ManifestDigest() != digest {
		t.Errorf("ManifestDigest() = %v, want %v", resolved.ManifestDigest(), digest)
	}

	if resolved.String() != ref.String() {
		t.Errorf("String() = %v, want %v", resolved.String(), ref.String())
	}
}

func TestResolvedRef_DigestHex(t *testing.T) {
	tests := []struct {
		name    string
		refStr  string
		digest  string
		wantHex string
	}{
		{
			name:    "tag-based ref uses manifest digest",
			refStr:  "alpine:latest",
			digest:  "sha256:1234567890abcdef",
			wantHex: "1234567890abcdef",
		},
		{
			name:    "digest-based ref uses manifest digest",
			refStr:  "alpine@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			digest:  "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			wantHex: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := Parse(tt.refStr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			resolved := NewResolvedRef(ref, tt.digest)

			got := resolved.DigestHex()
			if got != tt.wantHex {
				t.Errorf("DigestHex() = %v, want %v", got, tt.wantHex)
			}
		})
	}
}

func TestResolvedRef_DigestHex_ShadowsRef(t *testing.T) {
	// For tag-based refs, Ref.DigestHex() returns "" but
	// ResolvedRef.DigestHex() should return the resolved digest hex.
	ref, err := Parse("alpine:latest")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if ref.DigestHex() != "" {
		t.Fatalf("tag-based Ref.DigestHex() should be empty")
	}

	resolved := NewResolvedRef(ref, "sha256:abc123def456")

	if resolved.DigestHex() != "abc123def456" {
		t.Errorf("ResolvedRef.DigestHex() = %v, want abc123def456", resolved.DigestHex())
	}
}
