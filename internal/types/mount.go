// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"bytes"
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

	// MountTypeFile puts a file into the guest, with the contents the client
	// gave. It is written afresh at each start, so whatever the guest writes
	// to its copy is lost at its next start.
	MountTypeFile MountType = "file"

	// MountTypeTmpfs is an empty in-memory filesystem, lost when the guest stops.
	MountTypeTmpfs MountType = "tmpfs"
)

// MaxFileMountBytes is how much an instance's file mounts can hold between
// them. Their contents travel in requests and in the instance's definition,
// so anything larger belongs in a volume or the image.
const MaxFileMountBytes = 1 << 20

// defaultFileMountMode is the permission bits of a file mount given none.
const defaultFileMountMode = 0o644

// Mount attaches a volume, a file or a tmpfs at Target in the guest.
type Mount struct {
	Type MountType `yaml:"type" json:"type"`

	// Source is the volume's name for a volume. A file and a tmpfs have
	// none.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`

	// Target is the absolute path the mount appears at in the guest.
	Target string `yaml:"target" json:"target"`

	// ReadOnly stops the guest writing to the mount. A volume that every
	// instance attaching it mounts read-only can be shared between them.
	ReadOnly bool `yaml:"read_only,omitempty" json:"read_only,omitempty"`

	// Content is a file's contents. Only a file has them.
	Content []byte `yaml:"content,omitempty" json:"content,omitempty"`

	// Mode is a file's permission bits in the guest, where root owns it.
	// Zero is 0644.
	Mode uint32 `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// FileMode returns the permission bits a file mount's file has in the
// guest.
func (m Mount) FileMode() uint32 {
	if m.Mode == 0 {
		return defaultFileMountMode
	}
	return m.Mode
}

// Equal reports whether m and other attach the same thing at the same
// place, a file with the same contents.
func (m Mount) Equal(other Mount) bool {
	return m.Type == other.Type && m.Source == other.Source && m.Target == other.Target &&
		m.ReadOnly == other.ReadOnly && bytes.Equal(m.Content, other.Content) && m.Mode == other.Mode
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
// has, no volume is mounted twice, and the files hold no more than
// MaxFileMountBytes between them.
func validateMounts(mounts []Mount) error {
	targets := make(map[string]struct{}, len(mounts))
	volumes := make(map[string]struct{}, len(mounts))
	fileBytes := 0

	for _, m := range mounts {
		if err := m.validate(); err != nil {
			return err
		}

		fileBytes += len(m.Content)
		if fileBytes > MaxFileMountBytes {
			return errdefs.InvalidArgument(
				"the file mounts hold more than %d bytes between them: put larger files in a volume or the image",
				MaxFileMountBytes)
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

	if m.Type != MountTypeFile && (len(m.Content) > 0 || m.Mode != 0) {
		return errdefs.InvalidArgument("mount %s: only a file mount has contents and a mode", m)
	}

	switch m.Type {
	case MountTypeVolume:
		if m.Source == "" {
			return errdefs.InvalidArgument("mount %s: a volume mount needs the volume's name as its source", m)
		}
	case MountTypeFile:
		if m.Source != "" {
			return errdefs.InvalidArgument(
				"mount %s: a file mount has no source: the client sends the file's contents", m)
		}
		if m.Mode&^0o777 != 0 {
			return errdefs.InvalidArgument("mount %s: mode %#o is not permission bits", m, m.Mode)
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
