// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"path"
	"strings"

	"github.com/konradasb/dicer/internal/errdefs"
)

// MountType is what a Mount attaches.
type MountType string

const (
	// MountTypeVolume attaches a named volume: persistent storage that outlives
	// the instance, as a disk of its own.
	MountTypeVolume MountType = "volume"

	// MountTypeFile copies a host file into the guest. The file is read at each
	// start, so a change on the host reaches the guest at its next start,
	// and whatever the guest writes to its copy is lost at its next start.
	MountTypeFile MountType = "file"

	// MountTypeTmpfs is an empty in-memory filesystem, lost when the guest stops.
	MountTypeTmpfs MountType = "tmpfs"
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

// validateMounts returns an invalid argument error unless each mount has a
// known type, the source it needs, and an absolute target no other mount
// has, and no volume is mounted twice.
func validateMounts(mounts []Mount) error {
	targets := make(map[string]struct{}, len(mounts))
	volumes := make(map[string]struct{}, len(mounts))

	for _, m := range mounts {
		if err := m.validate(); err != nil {
			return err
		}

		target := path.Clean(m.Target)
		if _, dup := targets[target]; dup {
			return errdefs.InvalidArgument("two mounts have the target %q", target)
		}
		targets[target] = struct{}{}

		if m.Type == MountTypeVolume {
			if _, dup := volumes[m.Source]; dup {
				return errdefs.InvalidArgument("volume %q is mounted twice", m.Source)
			}
			volumes[m.Source] = struct{}{}
		}
	}

	return nil
}

func (m Mount) validate() error {
	switch {
	case m.Target == "":
		return errdefs.InvalidArgument("mount %s: it needs a target", m)
	case !path.IsAbs(m.Target):
		return errdefs.InvalidArgument("mount %s: the target %q must be absolute, e.g. /data", m, m.Target)
	case path.Clean(m.Target) == "/":
		return errdefs.InvalidArgument("mount %s: nothing can be mounted over the root filesystem", m)
	}

	switch m.Type {
	case MountTypeVolume:
		if m.Source == "" {
			return errdefs.InvalidArgument("mount %s: a volume mount needs the volume's name as its source", m)
		}
	case MountTypeFile:
		if !path.IsAbs(m.Source) {
			return errdefs.InvalidArgument("mount %s: a file mount needs an absolute host path as its source", m)
		}
	case MountTypeTmpfs:
		if m.Source != "" {
			return errdefs.InvalidArgument("mount %s: a tmpfs has no source", m)
		}
		if m.ReadOnly {
			return errdefs.InvalidArgument("mount %s: a read-only tmpfs would always be empty", m)
		}
	default:
		return errdefs.InvalidArgument("mount %s: unknown type %q: want %s, %s or %s",
			m, m.Type, MountTypeVolume, MountTypeFile, MountTypeTmpfs)
	}

	return nil
}
