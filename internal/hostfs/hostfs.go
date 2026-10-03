// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package hostfs finds the host's files from the daemon, whose view of the
// filesystem may not be the host's: systemd's PrivateTmp gives it a /tmp of
// its own, in a mount namespace of its own. A path a user gives, for a file
// or directory to mount, is the host's, and is looked for where the host
// has it.
package hostfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const (
	// MountNamespace is the host's mount namespace: PID 1's.
	MountNamespace = "/proc/1/ns/mnt"
	// hostRoot is the host's root directory, as PID 1 sees it, from another
	// mount namespace.
	hostRoot = "/proc/1/root"
)

// separate reports whether the daemon has a mount namespace of its own. It
// is worked out once: a process's mount namespace does not change.
var separate = sync.OnceValue(func() bool {
	return !sameMountNamespace("/proc/self/ns/mnt", MountNamespace)
})

// Separate reports whether the daemon has a mount namespace of its own,
// rather than the host's.
func Separate() bool { return separate() }

// Path returns where the daemon finds the host's path: path itself if the
// daemon sees the filesystem as the host does, and path under PID 1's root
// if not. path must be absolute.
func Path(path string) string {
	return pathIn(separate(), path)
}

func pathIn(separate bool, path string) string {
	if !separate || !filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(hostRoot, path)
}

// Stat is os.Stat of the host's path, with errors that name it as given.
func Stat(path string) (fs.FileInfo, error) {
	info, err := os.Stat(Path(path))
	return info, named(err, path)
}

// ReadFile is os.ReadFile of the host's path, with errors that name it as
// given.
func ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(Path(path))
	return data, named(err, path)
}

// ErrNotDirectory is CheckDir's error for a path that is not a directory.
var ErrNotDirectory = errors.New("not a directory")

// CheckDir returns an error, naming path as given, unless path is a
// directory on the host.
func CheckDir(path string) error {
	info, err := Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: %w", path, ErrNotDirectory)
	}
	return nil
}

// named makes a file error name path, rather than where the daemon found it.
func named(err error, path string) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return &fs.PathError{Op: pathErr.Op, Path: path, Err: pathErr.Err}
	}
	return err
}

// sameMountNamespace reports whether two processes' mount namespaces are
// one, from their /proc/PID/ns/mnt links. Links that cannot be read are taken
// to be the same: there is nothing to see past.
func sameMountNamespace(a, b string) bool {
	la, errA := os.Readlink(a)
	lb, errB := os.Readlink(b)
	return errA != nil || errB != nil || la == lb
}
