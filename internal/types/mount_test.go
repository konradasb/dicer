// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

func TestValidateMountsAcceptsValidMounts(t *testing.T) {
	err := validateMounts([]Mount{
		{Type: MountTypeVolume, Source: "data", Target: "/data/"},
		{Type: MountTypeFile, Content: []byte("a=1"), Mode: 0o600, Target: "/data/a"},
		{Type: MountTypeFile, Target: "/data/empty"},
		{Type: MountTypeTmpfs, Target: "/tmp//x"},
	})
	if err != nil {
		t.Errorf("validateMounts = %v, want nil", err)
	}
}

func TestValidateMountsRejectsInvalidMounts(t *testing.T) {
	tests := map[string][]Mount{
		"no type":            {{Source: "data", Target: "/data"}},
		"unknown type":       {{Type: "bind", Source: "/srv", Target: "/srv"}},
		"relative target":    {{Type: MountTypeTmpfs, Target: "data"}},
		"root target":        {{Type: MountTypeTmpfs, Target: "/"}},
		"volume no source":   {{Type: MountTypeVolume, Target: "/data"}},
		"file with a source": {{Type: MountTypeFile, Source: "/etc/shadow", Target: "/a"}},
		"file mode beyond permission bits": {
			{Type: MountTypeFile, Mode: 0o4755, Target: "/a"},
		},
		"volume with contents": {{Type: MountTypeVolume, Source: "v", Target: "/a", Content: []byte("x")}},
		"files over the limit": {
			{Type: MountTypeFile, Target: "/a", Content: make([]byte, MaxFileMountBytes/2+1)},
			{Type: MountTypeFile, Target: "/b", Content: make([]byte, MaxFileMountBytes/2)},
		},
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
			if err := validateMounts(mounts); !errors.Is(err, errdefs.ErrInvalidArgument) {
				t.Errorf("validateMounts(%+v) = %v, want an invalid argument error", mounts, err)
			}
		})
	}
}

func TestFileModeDefaultsTo0644(t *testing.T) {
	if got := (Mount{Type: MountTypeFile}).FileMode(); got != 0o644 {
		t.Errorf("FileMode of a mount with no mode = %#o, want 0644", got)
	}
	if got := (Mount{Type: MountTypeFile, Mode: 0o600}).FileMode(); got != 0o600 {
		t.Errorf("FileMode = %#o, want 0600", got)
	}
}

func TestMountEqualComparesContents(t *testing.T) {
	a := Mount{Type: MountTypeFile, Target: "/a", Content: []byte("one")}
	if !a.Equal(Mount{Type: MountTypeFile, Target: "/a", Content: []byte("one")}) {
		t.Error("mounts with the same contents are not equal")
	}
	if a.Equal(Mount{Type: MountTypeFile, Target: "/a", Content: []byte("two")}) {
		t.Error("mounts with other contents are equal")
	}
}
