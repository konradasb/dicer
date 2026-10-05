// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package diskfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
