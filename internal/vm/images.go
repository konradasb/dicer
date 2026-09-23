// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import "github.com/konradasb/dicer/internal/types"

// ImagesInUse returns the digests of images that must be kept: those
// instances are defined to boot from, those active guests booted from, and
// those snapshots need.
func (m *Manager) ImagesInUse() (map[string]struct{}, error) {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		return nil, err
	}

	inUse := make(map[string]struct{})
	for _, inst := range instances {
		if img, err := m.images.Get(inst.ImageRef); err == nil {
			inUse[img.Digest] = struct{}{}
		}

		rt, err := m.Runtime(inst)
		if err != nil {
			return nil, err
		}
		if rt.ImageDigest != "" && (rt.State.HoldsResources() || rt.State == types.StateStopping) {
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
