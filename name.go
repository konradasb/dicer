// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import "regexp"

// nameRe is what a resource name may look like: an RFC 1123 subdomain, which
// is dotted labels of letters, digits and hyphens. Names become file and
// directory names under the data directory, so this is what keeps a name
// like "../x" from escaping it -- neither ".." nor a leading dot is a valid
// label. An instance's name is also its default hostname, and this is a
// valid one.
var nameRe = regexp.MustCompile(
	`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// maxNameLen is the longest a name may be, as for a hostname.
const maxNameLen = 253

// ValidateName reports whether s is a valid resource name: dot-separated labels
// of letters, digits and hyphens, each starting and ending with a letter or
// digit -- "vmlinux-6.1", say.
func ValidateName(s string) error {
	if len(s) > maxNameLen || !nameRe.MatchString(s) {
		return InvalidArgument("invalid name %q: use letters, digits and hyphens, "+
			"in dot-separated parts that each start and end with a letter or digit", s)
	}

	return nil
}
