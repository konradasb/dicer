// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"errors"
	"fmt"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/types"
)

// Prune removes every image whose digest is not in keep, and the layers in
// the layer cache only they needed.
func (m *Manager) Prune(keep map[string]struct{}) (types.PruneResult, error) {
	var unused []*types.Image
	for _, image := range m.index.list() {
		if _, ok := keep[image.Digest]; !ok {
			unused = append(unused, image)
		}
	}
	result, err := m.remove(unused)
	for _, image := range result.Images {
		m.record(&image, events.ActionDeleted, fmt.Sprintf("Deleted image %s (%s) by prune: no instance uses it; %s boot disk removed",
			image.Name, reference.ShortDigest(image.Digest), humanize.Bytes(image.SizeBytes)), map[string]string{"by": "prune"})
	}
	return result, err
}

// remove deletes images and their disks, then the cached layers no remaining
// image needs. It is how every removal goes, asked for or collected.
func (m *Manager) remove(images []*types.Image) (types.PruneResult, error) {
	var result types.PruneResult

	for _, image := range images {
		if err := m.index.delete(image.Digest); err != nil {
			if errors.Is(err, errdefs.ErrNotFound) {
				continue // removed by someone else meanwhile
			}
			return result, err
		}
		if err := m.deleteFiles(digestHex(image.Digest)); err != nil {
			m.logger.Warn("failed to delete image files", "digest", image.Digest, "error", err)
			continue
		}

		result.Images = append(result.Images, *image)
		result.ReclaimedBytes += image.SizeBytes
	}

	// The layer cache is keyed by the same digests, so what remains in the
	// index is what it should keep.
	remaining := m.index.list()
	keep := make([]string, 0, len(remaining))
	for _, image := range remaining {
		keep = append(keep, digestHex(image.Digest))
	}

	reclaimed, err := m.registry.PruneCache(keep)
	if err != nil {
		return result, fmt.Errorf("prune layer cache: %w", err)
	}
	result.ReclaimedBytes += reclaimed

	return result, nil
}
