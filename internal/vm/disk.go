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

	"github.com/dicer-sh/dicer/internal/atomicfile"
	"github.com/dicer-sh/dicer/internal/guest"
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
// already exist. It is the instance's root filesystem, so a restart must
// reuse the existing disk rather than reformatting it and silently
// discarding everything the guest wrote.
//
// The disk is formatted under a temporary name and renamed into place, so
// its existence means it is formatted: a crash between allocating and
// formatting cannot leave behind a blank file that every later start would
// mistake for a disk.
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

// writeStatusDisk writes st to the disk the guest reports its end on,
// replacing whatever a previous boot left there. See guest.Status.
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

// configDiskSize is the size of the config disk: far more than a JSON file
// needs, and sparse, so the slack costs nothing.
const configDiskSize = 4 << 20

// provisionConfigDisk creates a small read-only ext4 disk image holding the
// single file dicer-init reads its configuration from on boot.
// Requires mke2fs (e2fsprogs >= 1.43) for the -d flag.
//
// The config carries the contents of injected host files, so it is staged
// next to the disk, in the instance's private runtime directory -- a tmpfs --
// rather than in the system temporary directory, and readable only by root.
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

	if err := createSparseFile(path, configDiskSize); err != nil {
		return fmt.Errorf("allocate config disk: %w", err)
	}

	out, err := exec.CommandContext(ctx, "mke2fs", "-t", "ext4", "-d", tmpDir, path).CombinedOutput()
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("format config disk: %w: %s", err, out)
	}
	return nil
}

// copyChunkSize is the unit copyDisk reads and writes, and the granularity at
// which it detects holes.
const copyChunkSize = 1 << 20

// copyDisk copies a disk image, sharing its blocks with the original where
// the filesystem can and skipping the holes where it cannot.
//
// A disk image is mostly empty and often large, so copying it naively costs
// its nominal size rather than its contents. On a copy-on-write filesystem
// the clone below makes the copy instant and free; anywhere else the zero
// runs are seeked over rather than written, which keeps the copy as sparse
// as the original.
func copyDisk(src, dst string) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	// Written under a temporary name and renamed, so an interrupted copy
	// cannot be mistaken for a whole disk.
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

	// The last block may have been a hole, and a hole at the end of a file
	// is just a shorter file until the size is set.
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

// dirSize returns what a directory tree occupies on disk.
//
// It counts allocated blocks rather than apparent sizes, because a snapshot
// holds sparse disk copies: a mostly empty 10 GiB overlay looks like 10 GiB
// to Stat and costs almost nothing, and reporting the former would be a lie
// about the cost of keeping the snapshot.
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

// statBlockSize is the unit st_blocks counts, which is 512 bytes whatever the
// filesystem's own block size is.
const statBlockSize = 512
