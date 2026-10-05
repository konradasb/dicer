// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import "github.com/konradasb/dicer/internal/types"

// ImagesInUse returns the digests of images that must be kept: those
// instances are defined to boot from, those active guests booted from, and
// those memory snapshots' guests booted from.
func (m *Manager) ImagesInUse() (map[string]struct{}, error) {
	inUse := make(map[string]struct{})
	for _, instance := range m.definitions.Instances() {
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
	}
	for _, snapshot := range m.definitions.Snapshots() {
		if snapshot.ImageDigest != "" {
			inUse[snapshot.ImageDigest] = struct{}{}
		}
	}

	return inUse, nil
}
