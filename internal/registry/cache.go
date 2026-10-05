// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	gcr "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/match"
	ispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// PruneCache drops every image from the layer cache but those keep names, by
// their manifest digests' hex, with the blobs no image left uses, and returns
// the bytes reclaimed.
func (c *Client) PruneCache(keep []string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	path, err := layout.FromPath(c.cacheDir)
	if err != nil {
		// No layout, nothing cached, nothing to reclaim.
		return 0, nil
	}

	kept := make(map[string]struct{}, len(keep))
	for _, hex := range keep {
		kept[hex] = struct{}{}
	}

	index, err := path.ImageIndex()
	if err != nil {
		return 0, fmt.Errorf("read index: %w", err)
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return 0, fmt.Errorf("read index manifest: %w", err)
	}

	// Drop the descriptors of images that are going, then work out which
	// blobs the survivors still need.
	var dropped []gcr.Hash
	reachable := make(map[string]struct{})

	for _, descriptor := range manifest.Manifests {
		if _, ok := kept[descriptor.Annotations[ispec.AnnotationRefName]]; !ok {
			dropped = append(dropped, descriptor.Digest)
			continue
		}

		if err := markReachable(path, descriptor.Digest, reachable); err != nil {
			return 0, err
		}
	}

	if len(dropped) == 0 {
		return 0, nil
	}

	for _, digest := range dropped {
		if err := path.RemoveDescriptors(match.Digests(digest)); err != nil {
			return 0, fmt.Errorf("remove index entry %s: %w", digest, err)
		}
	}

	return c.sweepBlobs(reachable)
}

// CacheSize returns what the layer cache occupies on disk.
func (c *Client) CacheSize() (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var total int64
	err := filepath.WalkDir(c.cacheDir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

// markReachable records the blobs an image is made of: its manifest, its
// configuration and its layers.
func markReachable(path layout.Path, digest gcr.Hash, reachable map[string]struct{}) error {
	reachable[digest.Hex] = struct{}{}

	image, err := path.Image(digest)
	if err != nil {
		return fmt.Errorf("open image %s: %w", digest, err)
	}

	manifest, err := image.Manifest()
	if err != nil {
		return fmt.Errorf("read manifest %s: %w", digest, err)
	}

	reachable[manifest.Config.Digest.Hex] = struct{}{}
	for _, l := range manifest.Layers {
		reachable[l.Digest.Hex] = struct{}{}
	}

	return nil
}

// sweepBlobs removes the blobs no remaining image refers to.
func (c *Client) sweepBlobs(reachable map[string]struct{}) (int64, error) {
	// Every digest Dicer deals with is a SHA-256 one.
	blobsDir := filepath.Join(c.cacheDir, "blobs", "sha256")

	entries, err := os.ReadDir(blobsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read blobs: %w", err)
	}

	var reclaimed int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := reachable[e.Name()]; ok {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(blobsDir, e.Name())); err != nil {
			c.logger.Warn("could not remove cached blob",
				slog.String("blob", e.Name()), slog.Any("error", err))
			continue
		}
		reclaimed += info.Size()
	}

	return reclaimed, nil
}
