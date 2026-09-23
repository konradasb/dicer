// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"sync"
	"time"

	"github.com/dicer-sh/dicer"
)

// index is the in-memory set of images this host holds, keyed by digest. It
// is rebuilt from disk at startup; the files themselves are paths.go's
// business.
type index struct {
	images map[string]*dicer.Image
	mu     sync.RWMutex
}

func newIndex() *index {
	return &index{images: make(map[string]*dicer.Image)}
}

// get retrieves an image by digest.
func (x *index) get(digest string) (*dicer.Image, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()

	img, ok := x.images[digest]
	return img, ok
}

// findByName returns the image most recently pulled under a reference,
// without consulting a registry. A tag that moved and was pulled again names
// several images; the latest pull is what the tag means on this host.
func (x *index) findByName(name string) (*dicer.Image, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()

	var found *dicer.Image
	for _, img := range x.images {
		if img.Name == name && (found == nil || img.CreatedAt.After(found.CreatedAt)) {
			found = img
		}
	}

	return found, found != nil
}

// list returns all images.
func (x *index) list() []*dicer.Image {
	x.mu.RLock()
	defer x.mu.RUnlock()

	images := make([]*dicer.Image, 0, len(x.images))
	for _, img := range x.images {
		images = append(images, img)
	}

	return images
}

// create adds a new image to the index. Its times are its own: set when it
// was pulled, and kept as they were recorded when it is loaded again.
func (x *index) create(img *dicer.Image) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if _, exists := x.images[img.Digest]; exists {
		return dicer.ErrExists
	}

	x.images[img.Digest] = img
	return nil
}

// markUsed records that an image was used at, if that is later than it was
// last known to be, and returns the updated image to be saved.
//
// Images handed out by the index are shared, so the update replaces the
// image with a copy rather than changing it under its readers.
func (x *index) markUsed(digest string, at time.Time) (*dicer.Image, bool) {
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
		return dicer.ErrNotFound
	}

	delete(x.images, digest)
	return nil
}
