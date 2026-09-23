// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/konradasb/dicer/internal/atomicfile"
	"github.com/konradasb/dicer/internal/guest"
)

// createSparseFile creates a sparse file of the given size at path.
// The file is created fresh; any existing file at path is truncated.
func createSparseFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// ensureOverlayDisk creates the writable ext4 overlay disk if it does not
// exist. It is formatted under a temporary name and renamed into place.
func ensureOverlayDisk(ctx context.Context, path string, sizeBytes int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	tmp := path + ".tmp"
	if err := createSparseFile(tmp, sizeBytes); err != nil {
		return fmt.Errorf("allocate overlay disk: %w", err)
	}

	out, err := exec.CommandContext(ctx, "mkfs.ext4", "-F", tmp).CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("format overlay disk: %w: %s", err, out)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install overlay disk: %w", err)
	}
	return nil
}

// writeStatusDisk writes st to the status disk. See guest.Status.
func writeStatusDisk(path string, st guest.Status) error {
	if err := atomicfile.Write(path, st.Encode(), 0o600); err != nil {
		return fmt.Errorf("write status disk: %w", err)
	}
	return nil
}

// readStatusDisk reads what the guest reported of its end.
func readStatusDisk(path string) (guest.Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return guest.Status{}, fmt.Errorf("read status disk: %w", err)
	}
	return guest.DecodeStatus(data)
}

// configDiskSize is the size of the sparse config disk, before room for the
// config itself, which holds the contents of every mounted host file.
const configDiskSize = 4 << 20

// provisionConfigDisk creates the ext4 disk holding dicer-init's
// configuration, using mke2fs -d (e2fsprogs >= 1.43). The config may hold
// secrets, so it is staged in the instance's runtime directory.
func provisionConfigDisk(ctx context.Context, path string, cfg *guest.Config) error {
	tmpDir, err := os.MkdirTemp(filepath.Dir(path), "config-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, guest.ConfigFile), data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", guest.ConfigFile, err)
	}

	if err := createSparseFile(path, configDiskSize+2*int64(len(data))); err != nil {
		return fmt.Errorf("allocate config disk: %w", err)
	}

	out, err := exec.CommandContext(ctx, "mke2fs", "-t", "ext4", "-d", tmpDir, path).CombinedOutput()
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("format config disk: %w: %s", err, out)
	}
	return nil
}

// copyChunkSize is the unit copySparse reads and detects holes in.
const copyChunkSize = 1 << 20

// copyDisk copies a disk image by reflink where supported, and otherwise as a
// sparse copy.
func copyDisk(src, dst string) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	defer func() {
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(tmp)
			return
		}
		err = os.Rename(tmp, dst)
	}()

	if cloneErr := cloneFile(in, out); cloneErr == nil {
		return nil
	}

	return copySparse(in, out)
}

// copySparse copies in to out, seeking over runs of zeroes rather than
// writing them, and sizing the result to match.
func copySparse(in, out *os.File) error {
	info, err := in.Stat()
	if err != nil {
		return err
	}

	buf := make([]byte, copyChunkSize)
	var offset int64

	for {
		n, err := in.ReadAt(buf, offset)
		if n > 0 {
			if block := buf[:n]; !isZero(block) {
				if _, err := out.WriteAt(block, offset); err != nil {
					return err
				}
			}
			offset += int64(n)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}

	// Set the size in case the file ends in a hole.
	return out.Truncate(info.Size())
}

// isZero reports whether a block is entirely zero bytes.
func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// dirSize returns the allocated size of a directory tree, which for sparse
// files is less than their apparent size.
func dirSize(dir string) (int64, error) {
	var total int64

	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}

		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			total += st.Blocks * statBlockSize
			return nil
		}
		total += info.Size()

		return nil
	})

	return total, err
}

// statBlockSize is the unit of st_blocks.
const statBlockSize = 512
