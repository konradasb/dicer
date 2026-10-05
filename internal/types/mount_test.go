// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

func TestValidateMountsCleansTargets(t *testing.T) {
	got, err := ValidateMounts([]Mount{
		{Type: MountTypeVolume, Source: "data", Target: "/data/"},
		{Type: MountTypeFile, Source: "/etc/a", Target: "/data/a"},
		{Type: MountTypeTmpfs, Target: "/tmp//x"},
	})
	if err != nil {
		t.Fatalf("ValidateMounts() = %v", err)
	}
	if got[0].Target != "/data" || got[2].Target != "/tmp/x" {
		t.Errorf("targets = %q, %q; want them cleaned", got[0].Target, got[2].Target)
	}
}

func TestValidateMountsRejectsInvalidMounts(t *testing.T) {
	tests := map[string][]Mount{
		"no type":          {{Source: "data", Target: "/data"}},
		"unknown type":     {{Type: "bind", Source: "/srv", Target: "/srv"}},
		"relative target":  {{Type: MountTypeTmpfs, Target: "data"}},
		"root target":      {{Type: MountTypeTmpfs, Target: "/"}},
		"volume no source": {{Type: MountTypeVolume, Target: "/data"}},
		"relative file":    {{Type: MountTypeFile, Source: "a", Target: "/a"}},
		"tmpfs with source": {
			{Type: MountTypeTmpfs, Source: "x", Target: "/a"},
		},
		"read-only tmpfs": {{Type: MountTypeTmpfs, Target: "/a", ReadOnly: true}},
		"same target twice": {
			{Type: MountTypeTmpfs, Target: "/a"},
			{Type: MountTypeTmpfs, Target: "/a/"},
		},
		"same volume twice": {
			{Type: MountTypeVolume, Source: "v", Target: "/a"},
			{Type: MountTypeVolume, Source: "v", Target: "/b"},
		},
	}

	for name, mounts := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateMounts(mounts); !errors.Is(err, errdefs.ErrInvalidArgument) {
				t.Errorf("ValidateMounts(%+v) = %v, want an invalid argument error", mounts, err)
			}
		})
	}
}
