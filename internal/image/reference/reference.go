// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package reference parses OCI image references and resolves tags to
// digests.
package reference

import (
	"strings"

	"github.com/distribution/reference"
)

// Ref represents a parsed and normalized OCI image reference.
type Ref struct {
	raw        string
	repository string
	tag        string
	digest     string
}

// Parse validates and normalizes an image reference.
func Parse(s string) (*Ref, error) {
	named, err := reference.ParseNormalizedNamed(s)
	if err != nil {
		return nil, err
	}

	ref := &Ref{
		repository: reference.Domain(named) + "/" + reference.Path(named),
	}

	// Handle digest references
	if canonical, ok := named.(reference.Canonical); ok {
		ref.digest = canonical.Digest().String()
		ref.raw = canonical.String()
		return ref, nil
	}

	// Handle tagged references (add :latest if missing)
	tagged := reference.TagNameOnly(named)
	if t, ok := tagged.(reference.Tagged); ok {
		ref.tag = t.Tag()
	}
	ref.raw = tagged.String()

	return ref, nil
}

// String returns the normalized reference.
func (r *Ref) String() string { return r.raw }

// Repository returns the repository portion of the reference.
func (r *Ref) Repository() string { return r.repository }

// Tag returns the tag, or "" if the reference is by digest.
func (r *Ref) Tag() string { return r.tag }

// Digest returns the digest, or "" if the reference is by tag.
func (r *Ref) Digest() string { return r.digest }

// HasDigest reports whether the reference names an immutable digest.
func (r *Ref) HasDigest() bool { return r.digest != "" }

// DigestHex returns the digest without its algorithm prefix, suitable for
// use as a directory name.
func (r *Ref) DigestHex() string {
	if r.digest == "" {
		return ""
	}
	_, hex, _ := strings.Cut(r.digest, ":")
	return hex
}

// Familiar returns a reference in its short form: busybox:latest for
// docker.io/library/busybox:latest. An invalid reference is returned as is.
func Familiar(s string) string {
	named, err := reference.ParseNormalizedNamed(s)
	if err != nil {
		return s
	}
	return reference.FamiliarString(named)
}
