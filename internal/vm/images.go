// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import "github.com/konradasb/dicer/internal/types"

// ImagesInUse returns the digests of images that must be kept: those
// instances are defined to boot from, those active guests booted from, and
// those snapshots need.
func (m *Manager) ImagesInUse() (map[string]struct{}, error) {
	instances := m.definitions.Instances()

	inUse := make(map[string]struct{})
	for _, instance := range instances {
		if image, err := m.images.Image(instance.ImageRef); err == nil {
			inUse[image.Digest] = struct{}{}
		}

		status, err := m.Status(instance)
		if err != nil {
			return nil, err
		}
		if status.ImageDigest != "" && (status.State.HoldsResources() || status.State == types.InstanceStateStopping) {
			inUse[status.ImageDigest] = struct{}{}
		}

		snapshots, err := m.Snapshots(instance)
		if err != nil {
			return nil, err
		}
		for _, snapshot := range snapshots {
			inUse[snapshot.ImageDigest] = struct{}{}
		}
	}

	return inUse, nil
}
