// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import "github.com/dicer-sh/dicer"

// ImagesInUse returns the digests of the images this host must keep: those
// the instances are defined to boot from, as their references resolve here
// now; those running guests were booted from, whose root disks they are,
// whatever the tags have moved to since; and those snapshots were taken on,
// which their restores need.
//
// It is the one answer to what may not be removed, for a prune and for
// garbage collection alike.
func (m *Manager) ImagesInUse() (map[string]struct{}, error) {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		return nil, err
	}

	inUse := make(map[string]struct{})
	for _, inst := range instances {
		// A reference to an image this host does not hold keeps nothing:
		// there is nothing to keep.
		if img, err := m.images.Get(inst.ImageRef); err == nil {
			inUse[img.Digest] = struct{}{}
		}

		rt, err := m.Runtime(inst)
		if err != nil {
			return nil, err
		}
		if rt.ImageDigest != "" && (rt.State.HoldsResources() || rt.State == dicer.StateStopping) {
			inUse[rt.ImageDigest] = struct{}{}
		}

		snapshots, err := m.ListSnapshots(inst)
		if err != nil {
			return nil, err
		}
		for _, snap := range snapshots {
			inUse[snap.ImageDigest] = struct{}{}
		}
	}

	return inUse, nil
}
