// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/types"
)

// The methods in this file are where an image's files live under the data
// directory, and how they are written and read: the disk the guest boots
// from, and the metadata recorded beside it. Which images exist is index's
// business. Every image is filed under its digest's hex part.

// digestHex returns a digest without its algorithm prefix.
func digestHex(digest string) string {
	if _, hex, ok := strings.Cut(digest, ":"); ok {
		return hex
	}
	return digest
}

// imagesDir returns the directory holding every image.
func (m *Manager) imagesDir() string {
	return filepath.Join(m.dataDir, "images")
}

// imageDir returns the directory holding one image.
func (m *Manager) imageDir(digestHex string) string {
	return filepath.Join(m.imagesDir(), digestHex)
}

// diskPath returns the path to an image's disk.
func (m *Manager) diskPath(digestHex string) string {
	return filepath.Join(m.imageDir(digestHex), "disk.img")
}

// metadataPath returns the path to an image's metadata.
func (m *Manager) metadataPath(digestHex string) string {
	return filepath.Join(m.imageDir(digestHex), "metadata.json")
}

// unpackDir returns the directory a pull unpacks an image's layers under
// before packing them into its disk.
func (m *Manager) unpackDir(digestHex string) string {
	return filepath.Join(m.dataDir, "tmp", digestHex)
}

// rootfsDir returns the directory a pull unpacks an image's root filesystem
// into.
func (m *Manager) rootfsDir(digestHex string) string {
	return filepath.Join(m.unpackDir(digestHex), "rootfs")
}

// createDirs creates the directories every image is stored under.
func (m *Manager) createDirs() error {
	for _, dir := range []string{m.dataDir, m.imagesDir(), filepath.Join(m.dataDir, "tmp")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}

// storedDigestHexes returns the digest hex of every image directory, or nil
// if there are none.
func (m *Manager) storedDigestHexes() ([]string, error) {
	entries, err := os.ReadDir(m.imagesDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read images directory: %w", err)
	}

	var hexes []string
	for _, entry := range entries {
		if entry.IsDir() {
			hexes = append(hexes, entry.Name())
		}
	}
	return hexes, nil
}

// ensureImageDir creates an image's directory if it does not exist.
func (m *Manager) ensureImageDir(digestHex string) error {
	return os.MkdirAll(m.imageDir(digestHex), 0o750)
}

// ensureRootfsDir creates an image's rootfs directory if it does not exist.
func (m *Manager) ensureRootfsDir(digestHex string) error {
	return os.MkdirAll(m.rootfsDir(digestHex), 0o750)
}

// removeUnpackDir removes what a pull unpacked. It is not an error if
// nothing was.
func (m *Manager) removeUnpackDir(digestHex string) error {
	return os.RemoveAll(m.unpackDir(digestHex))
}

// deleteFiles removes every file an image has, unpacked ones included. It
// is not an error if there are none.
func (m *Manager) deleteFiles(digestHex string) error {
	if err := os.RemoveAll(m.imageDir(digestHex)); err != nil {
		return fmt.Errorf("remove image directory: %w", err)
	}

	// Leftovers of an interrupted pull are not worth failing over.
	_ = m.removeUnpackDir(digestHex)

	return nil
}

// diskExists reports whether an image's disk is present.
func (m *Manager) diskExists(digestHex string) bool {
	_, err := os.Stat(m.diskPath(digestHex))
	return err == nil
}

// saveMetadata records image beside its disk.
func (m *Manager) saveMetadata(digestHex string, image *types.Image) error {
	data, err := json.MarshalIndent(image, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	return atomicfile.Write(m.metadataPath(digestHex), data, 0o644)
}

// loadMetadata reads the image recorded by saveMetadata.
func (m *Manager) loadMetadata(digestHex string) (*types.Image, error) {
	data, err := os.ReadFile(m.metadataPath(digestHex))
	if err != nil {
		return nil, fmt.Errorf("read metadata file: %w", err)
	}

	var image types.Image
	if err := json.Unmarshal(data, &image); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}

	return &image, nil
}
