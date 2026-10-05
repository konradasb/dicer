// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package archive packs one file or directory as a tar stream and unpacks it
// with cp's placement rules:
//
//   - into an existing directory: beneath it, under its own name;
//   - to a path that does not exist: at that path;
//   - over an existing file: in its place, if it is a file itself.
//
// Archives are untrusted, so entries are created through an os.Root on the
// destination and cannot escape it.
package archive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Pack writes src, a file or directory, to w as a tar archive with a single
// top-level entry. src is followed if it is a symlink; symlinks beneath it are
// kept as links. Devices, sockets and FIFOs are skipped.
func Pack(w io.Writer, src string) error {
	src = filepath.Clean(src)
	root := filepath.Base(src)

	// Followed, but still named as it was given: cp /etc/os-release . makes
	// ./os-release, whatever it links to.
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	src = resolved

	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	tw := tar.NewWriter(w)

	if !info.IsDir() {
		if err := addEntry(tw, src, root, info); err != nil {
			return err
		}
		return tw.Close()
	}

	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}

		return addEntry(tw, p, path.Join(root, filepath.ToSlash(rel)), info)
	})
	if err != nil {
		return err
	}

	return tw.Close()
}

// addEntry writes one file, directory or symlink to the archive under name.
func addEntry(tw *tar.Writer, p, name string, info fs.FileInfo) error {
	var link string
	switch {
	case info.Mode().IsRegular(), info.IsDir():
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		link = target
	default:
		return nil
	}

	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	header.Name = name
	if info.IsDir() {
		header.Name += "/"
	}
	// Owners mean nothing on the other machine: whoever unpacks owns what
	// is unpacked, as with cp.
	header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""

	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, err = io.Copy(tw, f)
	return err
}

// Unpack reads an archive written by Pack from r and puts what it holds at
// dest, following cp: see the package documentation.
func Unpack(r io.Reader, dest string) (err error) {
	tr := tar.NewReader(r)

	first, err := tr.Next()
	if errors.Is(err, io.EOF) {
		return errors.New("the archive is empty")
	}
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}

	top := topLevel(first.Name)
	isDir := first.Typeflag == tar.TypeDir

	dir, name, err := placement(filepath.Clean(dest), isDir)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	// Set the mode and times of created directories last, deepest first,
	// so writing their contents does not undo them.
	type createdDir struct {
		header *tar.Header
		rel    string
	}
	var dirs []createdDir
	defer func() {
		for _, dir := range slices.Backward(dirs) {
			if finishErr := finishDir(root, dir.header, dir.rel); finishErr != nil && err == nil {
				err = fmt.Errorf("%s: %w", dir.rel, finishErr)
			}
		}
	}()

	for header := first; ; {
		rel, err := entryPath(header.Name, top, name)
		if err != nil {
			return err
		}
		created, err := unpackEntry(root, tr, header, rel)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if created && header.Typeflag == tar.TypeDir {
			dirs = append(dirs, createdDir{header: header, rel: rel})
		}

		header, err = tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
	}
}

// finishDir gives a directory its mode and times, once its contents are in.
func finishDir(root *os.Root, header *tar.Header, rel string) error {
	if err := root.Chmod(rel, header.FileInfo().Mode().Perm()); err != nil {
		return err
	}

	return root.Chtimes(rel, header.ModTime, header.ModTime)
}

// placement decides where an archive's top-level entry lands: the directory
// it is created in, and its name there.
func placement(dest string, isDir bool) (dir, name string, err error) {
	info, err := os.Stat(dest)
	switch {
	case err == nil && info.IsDir():
		// Into it, under its own name.
		return dest, "", nil
	case err == nil && isDir:
		return "", "", fmt.Errorf("cannot copy a directory over the file %s", dest)
	case err == nil, errors.Is(err, fs.ErrNotExist):
		// In its place, or at it, renamed.
		parent := filepath.Dir(dest)
		if info, err := os.Stat(parent); err != nil || !info.IsDir() {
			return "", "", fmt.Errorf("%s is not a directory", parent)
		}
		return parent, filepath.Base(dest), nil
	default:
		return "", "", err
	}
}

// topLevel returns the first element of an entry name.
func topLevel(name string) string {
	top, _, _ := strings.Cut(strings.TrimPrefix(name, "./"), "/")
	return top
}

// entryPath returns an entry's destination relative to the unpack
// directory, with the top-level element replaced by to if set. It rejects
// entries outside the top-level one and paths that are not plain relative
// paths.
func entryPath(entry, top, to string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(entry, "./"))
	if !filepath.IsLocal(clean) || topLevel(clean) != top {
		return "", fmt.Errorf("archive entry %q is outside %q", entry, top)
	}
	if to == "" {
		return clean, nil
	}

	return to + strings.TrimPrefix(clean, top), nil
}

// unpackEntry creates one entry in root at rel, and reports whether it did:
// a directory that already exists is used as it is.
func unpackEntry(root *os.Root, tr *tar.Reader, header *tar.Header, rel string) (bool, error) {
	mode := header.FileInfo().Mode().Perm()

	switch header.Typeflag {
	case tar.TypeDir:
		return makeDir(root, rel)

	case tar.TypeReg:
		// Replaced rather than written through: if something already
		// there is a symlink, the copy takes its place instead of writing
		// wherever it points.
		if err := removeUnlessDir(root, rel); err != nil {
			return false, err
		}
		f, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
		if err != nil {
			return false, err
		}
		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()
			return false, err
		}
		if err := f.Close(); err != nil {
			return false, err
		}
		// The mode again: the one given to OpenFile is filtered by the
		// umask, and the copy should have the original's.
		if err := root.Chmod(rel, mode); err != nil {
			return false, err
		}
		return true, root.Chtimes(rel, header.ModTime, header.ModTime)

	case tar.TypeSymlink:
		if err := removeUnlessDir(root, rel); err != nil {
			return false, err
		}
		// A symlink may point anywhere; it is only a name. The root is
		// what keeps anything later from being written through it.
		return true, root.Symlink(header.Linkname, rel)

	default:
		// Pack writes nothing else; a foreign archive's devices and hard
		// links are left out rather than trusted.
		return false, nil
	}
}

// makeDir creates a directory, writable for now whatever the archive says --
// see Unpack -- unless one is already there.
func makeDir(root *os.Root, rel string) (bool, error) {
	info, err := root.Lstat(rel)
	if err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("cannot replace %s with a directory", rel)
		}
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}

	return true, root.Mkdir(rel, 0o700)
}

// removeUnlessDir removes whatever is at rel so that something else can be
// created there, unless it is a directory: a file is never copied over one,
// and trying is an error.
func removeUnlessDir(root *os.Root, rel string) error {
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("cannot replace directory %s with a file", rel)
	}

	return root.Remove(rel)
}
