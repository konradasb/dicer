// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package reference parses OCI image references and resolves tags to
// digests.
package reference

import (
	"strings"

	"github.com/distribution/reference"
)

// Ref is a parsed OCI image reference in its normalised form, such as
// docker.io/library/alpine:latest.
type Ref struct {
	normalised string
	repository string
	digest     string
}

// Parse validates and normalises an image reference: the registry and
// library path Docker assumes are filled in, and a reference with neither tag
// nor digest is tagged latest.
func Parse(s string) (*Ref, error) {
	named, err := reference.ParseNormalizedNamed(s)
	if err != nil {
		return nil, err
	}

	ref := &Ref{
		repository: reference.Domain(named) + "/" + reference.Path(named),
	}
	if canonical, ok := named.(reference.Canonical); ok {
		ref.digest = canonical.Digest().String()
		ref.normalised = canonical.String()
		return ref, nil
	}
	ref.normalised = reference.TagNameOnly(named).String()

	return ref, nil
}

// String returns the normalised reference.
func (r *Ref) String() string { return r.normalised }

// Repository returns the reference without its tag or digest, such as
// docker.io/library/alpine.
func (r *Ref) Repository() string { return r.repository }

// Digest returns the digest the reference names, or "" if it names a tag.
func (r *Ref) Digest() string { return r.digest }

// HasDigest reports whether the reference names an immutable digest.
func (r *Ref) HasDigest() bool { return r.digest != "" }

// FamiliarString returns a reference in its short form: busybox:latest for
// docker.io/library/busybox:latest. An invalid reference is returned as is.
func FamiliarString(s string) string {
	named, err := reference.ParseNormalizedNamed(s)
	if err != nil {
		return s
	}
	return reference.FamiliarString(named)
}

// ShortDigest abbreviates a digest for a person, as git does a commit:
// "sha256:1a2b3c4d5e6f". A digest too short to abbreviate, or without an
// algorithm, is returned as is.
func ShortDigest(digest string) string {
	algorithm, hex, ok := strings.Cut(digest, ":")
	if !ok || len(hex) <= 12 {
		return digest
	}
	return algorithm + ":" + hex[:12]
}
