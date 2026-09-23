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

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/types"
)

// The helpers below are where an image's files live under the data
// directory: the disk the guest boots from and the metadata recorded beside
// it. Which images exist is index's business.

// imageDir returns the directory holding one image.
func (m *Manager) imageDir(digestHex string) string {
	return filepath.Join(m.dataDir, "images", digestHex)
}

// diskPath returns the path to the disk image file.
func (m *Manager) diskPath(digestHex string) string {
	return filepath.Join(m.imageDir(digestHex), "disk.img")
}

// tmpRootfsPath returns a temporary directory for extracting layers.
func (m *Manager) tmpRootfsPath(digestHex string) string {
	return filepath.Join(m.dataDir, "tmp", digestHex, "rootfs")
}

// metadataPath returns the path to the metadata JSON file.
func (m *Manager) metadataPath(digestHex string) string {
	return filepath.Join(m.imageDir(digestHex), "metadata.json")
}

// listImageDirs returns all image directories.
func (m *Manager) listImageDirs() ([]string, error) {
	imagesDir := filepath.Join(m.dataDir, "images")

	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read images dir: %w", err)
	}

	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirs = append(dirs, entry.Name())
	}

	return dirs, nil
}

// ensureImageDir creates the image directory if it doesn't exist.
func (m *Manager) ensureImageDir(digestHex string) error {
	dir := m.imageDir(digestHex)
	return os.MkdirAll(dir, 0o750)
}

// ensureTmpDir creates a temporary directory for extraction.
func (m *Manager) ensureTmpDir(digestHex string) error {
	dir := m.tmpRootfsPath(digestHex)
	return os.MkdirAll(dir, 0o750)
}

// cleanupTmpDir removes the temporary extraction directory.
func (m *Manager) cleanupTmpDir(digestHex string) error {
	tmpDir := filepath.Join(m.dataDir, "tmp", digestHex)
	return os.RemoveAll(tmpDir)
}

// deleteImage removes all files for an image.
func (m *Manager) deleteImage(digestHex string) error {
	imageDir := m.imageDir(digestHex)
	if err := os.RemoveAll(imageDir); err != nil {
		return fmt.Errorf("remove image directory: %w", err)
	}

	// Also clean up any leftover tmp directories
	_ = m.cleanupTmpDir(digestHex)

	return nil
}

// diskExists checks if the disk image file exists.
func (m *Manager) diskExists(digestHex string) bool {
	path := m.diskPath(digestHex)
	_, err := os.Stat(path)
	return err == nil
}

// saveMetadata writes image metadata to a JSON file.
func (m *Manager) saveMetadata(digestHex string, img *types.Image) error {
	data, err := json.MarshalIndent(img, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	return atomicfile.Write(m.metadataPath(digestHex), data, 0o644)
}

// loadMetadata reads image metadata from a JSON file.
func (m *Manager) loadMetadata(digestHex string) (*types.Image, error) {
	path := m.metadataPath(digestHex)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read metadata file: %w", err)
	}

	var img types.Image
	if err := json.Unmarshal(data, &img); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}

	return &img, nil
}

// initialize creates the base directory structure.
func (m *Manager) initialize() error {
	dirs := []string{
		m.dataDir,
		filepath.Join(m.dataDir, "images"),
		filepath.Join(m.dataDir, "tmp"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	return nil
}
