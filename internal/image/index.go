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
// is rebuilt from disk at startup; the files themselves are files.go's
// business. It is safe for concurrent use.
type index struct {
	mu     sync.RWMutex
	images map[string]*types.Image
}

// newIndex returns an empty index.
func newIndex() *index {
	return &index{images: make(map[string]*types.Image)}
}

// get returns the image with digest, and whether there is one.
func (x *index) get(digest string) (*types.Image, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()

	image, ok := x.images[digest]
	return image, ok
}

// latestByName returns the image most recently pulled under a reference,
// and whether there is one. A tag that moved and was pulled again names
// several images; the latest pull is what the tag means on this host.
func (x *index) latestByName(name string) (*types.Image, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()

	var found *types.Image
	for _, image := range x.images {
		if image.Name == name && (found == nil || image.CreatedAt.After(found.CreatedAt)) {
			found = image
		}
	}

	return found, found != nil
}

// list returns all images.
func (x *index) list() []*types.Image {
	x.mu.RLock()
	defer x.mu.RUnlock()

	images := make([]*types.Image, 0, len(x.images))
	for _, image := range x.images {
		images = append(images, image)
	}

	return images
}

// create adds a new image to the index, or returns errdefs.ErrExists if one
// with its digest is there. Its times are its own: set when it was pulled,
// and kept as they were recorded when it is loaded again.
func (x *index) create(image *types.Image) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if _, exists := x.images[image.Digest]; exists {
		return errdefs.ErrExists
	}

	x.images[image.Digest] = image
	return nil
}

// markUsed records that the image with digest was used at at, if later than
// recorded, and returns the updated copy to save. It returns false if
// nothing changed.
func (x *index) markUsed(digest string, at time.Time) (*types.Image, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()

	image, ok := x.images[digest]
	if !ok || !at.After(image.LastUsedAt) {
		return nil, false
	}

	updated := *image
	updated.LastUsedAt = at
	x.images[digest] = &updated
	return &updated, true
}

// delete removes an image from the index, or returns errdefs.ErrNotFound if
// it is not there.
func (x *index) delete(digest string) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if _, exists := x.images[digest]; !exists {
		return errdefs.ErrNotFound
	}

	delete(x.images, digest)
	return nil
}
