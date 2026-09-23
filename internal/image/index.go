// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"sync"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// index is the in-memory set of images this host holds, keyed by digest. It
// is rebuilt from disk at startup; the files themselves are paths.go's
// business.
type index struct {
	images map[string]*types.Image
	mu     sync.RWMutex
}

func newIndex() *index {
	return &index{images: make(map[string]*types.Image)}
}

// get retrieves an image by digest.
func (x *index) get(digest string) (*types.Image, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()

	img, ok := x.images[digest]
	return img, ok
}

// findByName returns the image most recently pulled under a reference,
// without consulting a registry. A tag that moved and was pulled again names
// several images; the latest pull is what the tag means on this host.
func (x *index) findByName(name string) (*types.Image, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()

	var found *types.Image
	for _, img := range x.images {
		if img.Name == name && (found == nil || img.CreatedAt.After(found.CreatedAt)) {
			found = img
		}
	}

	return found, found != nil
}

// list returns all images.
func (x *index) list() []*types.Image {
	x.mu.RLock()
	defer x.mu.RUnlock()

	images := make([]*types.Image, 0, len(x.images))
	for _, img := range x.images {
		images = append(images, img)
	}

	return images
}

// create adds a new image to the index. Its times are its own: set when it
// was pulled, and kept as they were recorded when it is loaded again.
func (x *index) create(img *types.Image) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if _, exists := x.images[img.Digest]; exists {
		return errdefs.ErrExists
	}

	x.images[img.Digest] = img
	return nil
}

// markUsed records that an image was used at the given time, if later than
// recorded, and returns the updated copy to save.
func (x *index) markUsed(digest string, at time.Time) (*types.Image, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()

	img, ok := x.images[digest]
	if !ok || !at.After(img.LastUsedAt) {
		return nil, false
	}

	updated := *img
	updated.LastUsedAt = at
	x.images[digest] = &updated
	return &updated, true
}

// delete removes an image from the index.
func (x *index) delete(digest string) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if _, exists := x.images[digest]; !exists {
		return errdefs.ErrNotFound
	}

	delete(x.images, digest)
	return nil
}
