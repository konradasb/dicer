// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hypervisor

import "slices"

// DeprecatedVersions returns the deprecated versions among starters, which
// list one hypervisor's versions with the default first. By Dicer's
// deprecation policy, every version but the default is deprecated, and is
// kept only for what still uses it.
func DeprecatedVersions(starters []Starter) []string {
	if len(starters) < 2 {
		return nil
	}

	versions := make([]string, 0, len(starters)-1)
	for _, s := range starters[1:] {
		versions = append(versions, s.Version())
	}
	return versions
}

// IsDeprecated reports whether version is a deprecated version among
// starters, listed as DeprecatedVersions takes them. An empty version means
// the default, which is never deprecated.
func IsDeprecated(starters []Starter, version string) bool {
	return slices.Contains(DeprecatedVersions(starters), version)
}
