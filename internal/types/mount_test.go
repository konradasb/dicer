// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"testing"
)

func TestValidateMounts(t *testing.T) {
	got, err := ValidateMounts([]Mount{
		{Type: MountVolume, Source: "data", Target: "/data/"},
		{Type: MountFile, Source: "/etc/a", Target: "/data/a"},
		{Type: MountTmpfs, Target: "/tmp//x"},
	})
	if err != nil {
		t.Fatalf("ValidateMounts: %v", err)
	}
	if got[0].Target != "/data" || got[2].Target != "/tmp/x" {
		t.Errorf("targets = %q, %q; want them cleaned", got[0].Target, got[2].Target)
	}

	for name, mounts := range map[string][]Mount{
		"no type":           {{Source: "data", Target: "/data"}},
		"unknown type":      {{Type: "bind", Source: "/srv", Target: "/srv"}},
		"relative target":   {{Type: MountTmpfs, Target: "data"}},
		"root target":       {{Type: MountTmpfs, Target: "/"}},
		"volume no source":  {{Type: MountVolume, Target: "/data"}},
		"relative file":     {{Type: MountFile, Source: "a", Target: "/a"}},
		"tmpfs with source": {{Type: MountTmpfs, Source: "x", Target: "/a"}},
		"read-only tmpfs":   {{Type: MountTmpfs, Target: "/a", ReadOnly: true}},
		"same target twice": {{Type: MountTmpfs, Target: "/a"}, {Type: MountTmpfs, Target: "/a/"}},
		"same volume twice": {{Type: MountVolume, Source: "v", Target: "/a"}, {Type: MountVolume, Source: "v", Target: "/b"}},
	} {
		if _, err := ValidateMounts(mounts); err == nil {
			t.Errorf("%s: ValidateMounts(%+v) should fail", name, mounts)
		}
	}
}
