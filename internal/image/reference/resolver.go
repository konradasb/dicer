// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package reference

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Resolver looks up the digest of the manifest a tagged reference names.
type Resolver interface {
	Resolve(ctx context.Context, ref *Ref) (string, error)
}

// ResolvedRef is an image reference pinned to a manifest digest.
type ResolvedRef struct {
	ref    *Ref
	digest string
}

// String returns the normalised reference, as it was before it was resolved.
func (r *ResolvedRef) String() string { return r.ref.String() }

// Digest returns the manifest digest the reference was resolved to.
func (r *ResolvedRef) Digest() string { return r.digest }

// DigestHex returns the digest without its algorithm prefix, suitable for
// use as a directory name.
func (r *ResolvedRef) DigestHex() string {
	_, hex, _ := strings.Cut(r.digest, ":")
	return hex
}

// Resolve pins ref to a manifest digest: its own if it names one, otherwise
// the one resolver finds for its tag. resolver may be nil only for a
// reference by digest.
func Resolve(ctx context.Context, resolver Resolver, ref *Ref) (*ResolvedRef, error) {
	if ref.HasDigest() {
		return &ResolvedRef{ref: ref, digest: ref.Digest()}, nil
	}

	if resolver == nil {
		return nil, errors.New("resolver is required for tag-based references")
	}
	digest, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve manifest: %w", err)
	}

	return &ResolvedRef{ref: ref, digest: digest}, nil
}
