// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package reference

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Resolver resolves image references to manifest digests.
type Resolver interface {
	Resolve(ctx context.Context, ref *Ref) (string, error)
}

// ResolvedRef is an image reference with a resolved manifest digest.
type ResolvedRef struct {
	*Ref
	manifestDigest string
}

// NewResolvedRef creates a resolved reference.
func NewResolvedRef(ref *Ref, digest string) *ResolvedRef {
	rRef := &ResolvedRef{
		Ref:            ref,
		manifestDigest: digest,
	}

	return rRef
}

// ManifestDigest returns the resolved manifest digest.
func (r *ResolvedRef) ManifestDigest() string {
	return r.manifestDigest
}

// DigestHex returns the hex portion of the resolved manifest digest.
// This shadows Ref.DigestHex() to use the resolved digest instead of the
// original reference's digest, which may be empty for tag-based references.
func (r *ResolvedRef) DigestHex() string {
	_, hex, _ := strings.Cut(r.manifestDigest, ":")
	return hex
}

// Resolve resolves the reference using the provided resolver.
func Resolve(ctx context.Context, resolver Resolver, refStr string) (*ResolvedRef, error) {
	ref, err := Parse(refStr)
	if err != nil {
		return nil, fmt.Errorf("parse reference: %w", err)
	}

	// If already has digest, use it directly
	if ref.HasDigest() {
		return NewResolvedRef(ref, ref.Digest()), nil
	}

	// Otherwise resolve via registry
	if resolver == nil {
		return nil, errors.New("resolver is required for tag-based references")
	}
	digest, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve manifest: %w", err)
	}

	return NewResolvedRef(ref, digest), nil
}
