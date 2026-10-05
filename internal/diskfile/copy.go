// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package diskfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// copyChunkSize is the unit copySparse reads and detects holes in.
const copyChunkSize = 1 << 20

// Copy copies the disk file at src to dst, sharing its blocks by reflink
// where the filesystem supports it and otherwise keeping its holes. The copy
// is written beside dst and renamed into place, so dst never holds a disk
// half copied.
func Copy(src, dst string) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create disk directory: %w", err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	partial := dst + ".tmp"
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", partial, err)
	}
	defer func() {
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(partial)
			return
		}
		err = os.Rename(partial, dst)
	}()

	if reflinkErr := reflink(in, out); reflinkErr == nil {
		return nil
	}

	return copySparse(in, out)
}

// copySparse copies in to out, keeping its holes: it reads only the extents
// the filesystem reports holding data, and within them leaves runs of zeroes
// unwritten. A disk is mostly holes, so reading them too would take as long
// as reading the whole disk. The result is sized to match.
func copySparse(in, out *os.File) error {
	info, err := in.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	buf := make([]byte, copyChunkSize)

	for offset := int64(0); offset < size; {
		start, err := in.Seek(offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			break // nothing but a hole from offset on
		}
		if err != nil {
			// The filesystem cannot say where the data is: read it all.
			return copyRange(in, out, buf, offset, size)
		}
		end, err := in.Seek(start, unix.SEEK_HOLE)
		if err != nil {
			end = size
		}
		if err := copyRange(in, out, buf, start, end); err != nil {
			return err
		}
		offset = end
	}

	// Set the size in case the file ends in a hole.
	return out.Truncate(size)
}

// copyRange copies in's bytes from start to end into out, leaving chunks of
// zeroes unwritten, using buf to hold each chunk.
func copyRange(in, out *os.File, buf []byte, start, end int64) error {
	for offset := start; offset < end; {
		n, err := in.ReadAt(buf[:min(int64(len(buf)), end-offset)], offset)
		if n > 0 {
			if block := buf[:n]; !isZero(block) {
				if _, err := out.WriteAt(block, offset); err != nil {
					return err
				}
			}
			offset += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
