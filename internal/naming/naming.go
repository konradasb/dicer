// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package naming is the rule every name Dicer gives a resource follows: an
// instance's, a network's, a snapshot's, a remote's.
package naming

import (
	"regexp"

	"github.com/konradasb/dicer/internal/errdefs"
)

// nameRe matches an RFC 1123 subdomain. Names become paths under the data
// directory, so this also rules out ".." and leading dots.
var nameRe = regexp.MustCompile(
	`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// maxNameLen is the longest a name may be, as for a hostname.
const maxNameLen = 253

// Validate reports whether s is a valid resource name, such as
// "vmlinux-6.1".
func Validate(s string) error {
	if len(s) > maxNameLen || !nameRe.MatchString(s) {
		return errdefs.InvalidArgument("invalid name %q: use letters, digits and hyphens, "+
			"in dot-separated parts that each start and end with a letter or digit", s)
	}

	return nil
}
