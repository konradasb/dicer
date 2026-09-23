// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"fmt"
	"path"
	"strings"
)

// MountType is what a Mount attaches.
type MountType string

const (
	// MountVolume attaches a named volume: persistent storage that outlives
	// the instance, as a disk of its own.
	MountVolume MountType = "volume"

	// MountFile copies a host file into the guest. The file is read at each
	// start, so a change on the host reaches the guest at its next start,
	// and whatever the guest writes to its copy is lost at its next start.
	MountFile MountType = "file"

	// MountTmpfs is an empty in-memory filesystem, lost when the guest stops.
	MountTmpfs MountType = "tmpfs"
)

// Mount attaches a volume, a host file or a tmpfs at Target in the guest.
type Mount struct {
	Type MountType `yaml:"type" json:"type"`

	// Source is the volume's name for a volume and the host file's absolute
	// path for a file. A tmpfs has none.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`

	// Target is the absolute path the mount appears at in the guest.
	Target string `yaml:"target" json:"target"`

	// ReadOnly stops the guest writing to the mount. A volume that every
	// instance attaching it mounts read-only can be shared between them.
	ReadOnly bool `yaml:"read_only,omitempty" json:"read_only,omitempty"`
}

// String renders the mount as --mount takes it.
func (m Mount) String() string {
	parts := []string{"type=" + string(m.Type)}
	if m.Source != "" {
		parts = append(parts, "source="+m.Source)
	}
	parts = append(parts, "target="+m.Target)
	if m.ReadOnly {
		parts = append(parts, "readonly")
	}
	return strings.Join(parts, ",")
}

// ValidateMounts checks what can be checked of mounts without the host: each
// has a known type, the source it needs, and an absolute target no other
// mount has. It returns the mounts with their targets cleaned.
func ValidateMounts(mounts []Mount) ([]Mount, error) {
	out := make([]Mount, 0, len(mounts))
	targets := make(map[string]struct{}, len(mounts))
	volumes := make(map[string]struct{}, len(mounts))

	for _, m := range mounts {
		if err := m.validate(); err != nil {
			return nil, err
		}
		m.Target = path.Clean(m.Target)

		if _, dup := targets[m.Target]; dup {
			return nil, fmt.Errorf("two mounts have the target %q", m.Target)
		}
		targets[m.Target] = struct{}{}

		if m.Type == MountVolume {
			if _, dup := volumes[m.Source]; dup {
				return nil, fmt.Errorf("volume %q is mounted twice", m.Source)
			}
			volumes[m.Source] = struct{}{}
		}

		out = append(out, m)
	}

	return out, nil
}

func (m Mount) validate() error {
	switch {
	case m.Target == "":
		return fmt.Errorf("mount %s: it needs a target", m)
	case !path.IsAbs(m.Target):
		return fmt.Errorf("mount %s: the target %q must be absolute, e.g. /data", m, m.Target)
	case path.Clean(m.Target) == "/":
		return fmt.Errorf("mount %s: nothing can be mounted over the root filesystem", m)
	}

	switch m.Type {
	case MountVolume:
		if m.Source == "" {
			return fmt.Errorf("mount %s: a volume mount needs the volume's name as its source", m)
		}
	case MountFile:
		if !path.IsAbs(m.Source) {
			return fmt.Errorf("mount %s: a file mount needs an absolute host path as its source", m)
		}
	case MountTmpfs:
		if m.Source != "" {
			return fmt.Errorf("mount %s: a tmpfs has no source", m)
		}
		if m.ReadOnly {
			return fmt.Errorf("mount %s: a read-only tmpfs would always be empty", m)
		}
	default:
		return fmt.Errorf("mount %s: unknown type %q: want %s, %s or %s",
			m, m.Type, MountVolume, MountFile, MountTmpfs)
	}

	return nil
}

// MountsVolume returns the mount by which the instance attaches the named
// volume, if it does.
func (s InstanceSpec) MountsVolume(name string) (Mount, bool) {
	for _, m := range s.Mounts {
		if m.Type == MountVolume && m.Source == name {
			return m, true
		}
	}
	return Mount{}, false
}
