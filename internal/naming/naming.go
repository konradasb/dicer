// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package naming is the rule every name Dicer gives a resource follows: an
// instance's, a network's, a snapshot's, a remote's; and a guest's
// hostname, which follows the same rule.
package naming

import (
	"regexp"

	"github.com/konradasb/dicer/internal/errdefs"
)

// namePattern matches an RFC 1123 subdomain. Names become paths under the
// data directory, so this also rules out ".." and leading dots.
var namePattern = regexp.MustCompile(
	`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// maxNameLength is the longest a name may be, as for a hostname.
const maxNameLength = 253

// nameRule says what namePattern allows.
const nameRule = "use letters, digits and hyphens, in dot-separated parts that each start and end with a letter or digit"

// Validate returns an error in the errdefs.ErrInvalidArgument class if name
// is not a valid resource name, such as "vmlinux-6.1", and nil if it is.
func Validate(name string) error {
	if len(name) > maxNameLength || !namePattern.MatchString(name) {
		return errdefs.InvalidArgument("invalid name %q: %s", name, nameRule)
	}

	return nil
}

// ValidateHostname returns an error in the errdefs.ErrInvalidArgument class
// if hostname is set and is not valid under RFC 1123, and nil if it is or
// is empty.
func ValidateHostname(hostname string) error {
	switch {
	case hostname == "":
		return nil
	case len(hostname) > maxNameLength:
		return errdefs.InvalidArgument("invalid hostname %q: it exceeds %d characters", hostname, maxNameLength)
	case !namePattern.MatchString(hostname):
		return errdefs.InvalidArgument("invalid hostname %q: %s", hostname, nameRule)
	}
	return nil
}
