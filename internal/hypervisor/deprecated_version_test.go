// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hypervisor

import (
	"slices"
	"testing"
)

// versionStarter is a Starter that knows only its version.
type versionStarter struct {
	Starter
	version string
}

func (s versionStarter) Version() string { return s.version }

func TestEveryVersionButTheDefaultIsDeprecated(t *testing.T) {
	starters := []Starter{versionStarter{version: "v53.0.0"}, versionStarter{version: "v49.0.0"}, versionStarter{version: "v48.0.0"}}

	if got, want := DeprecatedVersions(starters), []string{"v49.0.0", "v48.0.0"}; !slices.Equal(got, want) {
		t.Errorf("DeprecatedVersions = %v, want %v", got, want)
	}
	if got := DeprecatedVersions(starters[:1]); len(got) != 0 {
		t.Errorf("DeprecatedVersions of the default alone = %v, want none", got)
	}

	for version, want := range map[string]bool{
		"":        false, // the default
		"v53.0.0": false,
		"v49.0.0": true,
		"v48.0.0": true,
		"v47.0.0": false, // not carried at all
	} {
		if got := IsDeprecated(starters, version); got != want {
			t.Errorf("IsDeprecated(%q) = %t, want %t", version, got, want)
		}
	}
}
