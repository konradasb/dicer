// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"errors"
	"fmt"

	"github.com/dicer-sh/dicer"
)

// Prune removes every image whose digest is not in keep, along with the
// cached layers of the images it removes.
//
// What to keep is the caller's decision: this package knows which images
// exist, not which of them anything is using.
func (m *Manager) Prune(keep map[string]struct{}) (dicer.PruneResult, error) {
	var unused []*dicer.Image
	for _, img := range m.index.list() {
		if _, ok := keep[img.Digest]; !ok {
			unused = append(unused, img)
		}
	}
	result, err := m.remove(unused)
	for _, img := range result.Images {
		m.record(&img, dicer.ActionDeleted, fmt.Sprintf("Deleted image %s (%s) by prune: no instance uses it; %s boot disk removed",
			img.Name, shortDigest(img.Digest), humanSize(img.SizeBytes)), map[string]string{"by": "prune"})
	}
	return result, err
}

// remove deletes images and their disks, then the cached layers no remaining
// image needs. It is how every removal goes, asked for or collected.
func (m *Manager) remove(images []*dicer.Image) (dicer.PruneResult, error) {
	var result dicer.PruneResult

	for _, img := range images {
		if err := m.index.delete(img.Digest); err != nil {
			if errors.Is(err, dicer.ErrNotFound) {
				continue // removed by someone else meanwhile
			}
			return result, err
		}
		if err := m.deleteImage(digestHex(img.Digest)); err != nil {
			m.logger.Warn("could not remove image files", "digest", img.Digest, "error", err)
			continue
		}

		result.Images = append(result.Images, *img)
		result.ReclaimedBytes += img.SizeBytes
	}

	// The layer cache is keyed by the same digests, so what remains in the
	// index is what it should keep.
	remaining := m.index.list()
	cached := make([]string, 0, len(remaining))
	for _, img := range remaining {
		cached = append(cached, digestHex(img.Digest))
	}

	reclaimed, err := m.registry.PruneCache(cached)
	if err != nil {
		return result, fmt.Errorf("prune layer cache: %w", err)
	}
	result.ReclaimedBytes += reclaimed

	return result, nil
}
