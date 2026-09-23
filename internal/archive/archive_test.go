// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package archive

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// copyPath packs src and unpacks it at dest, as the two ends of a copy do.
func copyPath(t *testing.T, src, dest string) error {
	t.Helper()

	var buf bytes.Buffer
	if err := Pack(&buf, src); err != nil {
		t.Fatalf("Pack(%s): %v", src, err)
	}

	return Unpack(&buf, dest)
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// srcTree builds a directory with a nested file, an executable, a symlink
// and an empty directory, and returns it.
func srcTree(t *testing.T) string {
	t.Helper()

	src := filepath.Join(t.TempDir(), "app")
	writeFile(t, filepath.Join(src, "config.yaml"), "port: 80\n", 0o600)
	writeFile(t, filepath.Join(src, "bin", "run"), "#!/bin/sh\n", 0o755)
	if err := os.Symlink("bin/run", filepath.Join(src, "start")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}

	return src
}

func TestCopyDirectoryIntoExistingDirectory(t *testing.T) {
	src := srcTree(t)
	dest := t.TempDir()

	if err := copyPath(t, src, dest); err != nil {
		t.Fatalf("Unpack: %v", err)
	}

	// Beneath it, under its own name, as cp -r would.
	got := filepath.Join(dest, "app")
	if content := readFile(t, filepath.Join(got, "config.yaml")); content != "port: 80\n" {
		t.Errorf("config.yaml = %q", content)
	}

	// Modes survive, the umask notwithstanding.
	for path, want := range map[string]os.FileMode{
		"config.yaml": 0o600,
		"bin/run":     0o755,
		"empty":       0o750,
	} {
		info, err := os.Stat(filepath.Join(got, path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
	}

	// Symlinks are copied as symlinks.
	if target, err := os.Readlink(filepath.Join(got, "start")); err != nil || target != "bin/run" {
		t.Errorf("start -> %q (%v), want a symlink to bin/run", target, err)
	}
}

func TestCopyToNewPathRenames(t *testing.T) {
	src := srcTree(t)
	dest := filepath.Join(t.TempDir(), "renamed")

	if err := copyPath(t, src, dest); err != nil {
		t.Fatalf("Unpack: %v", err)
	}

	if content := readFile(t, filepath.Join(dest, "bin", "run")); content != "#!/bin/sh\n" {
		t.Errorf("renamed/bin/run = %q", content)
	}
}

func TestCopyFileOverFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "new.txt")
	writeFile(t, src, "new", 0o644)
	dest := filepath.Join(t.TempDir(), "old.txt")
	writeFile(t, dest, "old contents, longer", 0o644)

	if err := copyPath(t, src, dest); err != nil {
		t.Fatalf("Unpack: %v", err)
	}

	if content := readFile(t, dest); content != "new" {
		t.Errorf("old.txt = %q, want it replaced", content)
	}
}

func TestCopyFilePreservesModTime(t *testing.T) {
	src := filepath.Join(t.TempDir(), "f")
	writeFile(t, src, "x", 0o644)
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()

	if err := copyPath(t, src, dest); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dest, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(mtime) {
		t.Errorf("mtime = %s, want %s", info.ModTime(), mtime)
	}
}

func TestCopyDirectoryOverFileIsRefused(t *testing.T) {
	src := srcTree(t)
	dest := filepath.Join(t.TempDir(), "file")
	writeFile(t, dest, "keep me", 0o644)

	if err := copyPath(t, src, dest); err == nil {
		t.Fatal("a directory was copied over a file")
	}
	if content := readFile(t, dest); content != "keep me" {
		t.Errorf("the file was changed to %q", content)
	}
}

func TestCopyIntoMissingParentIsRefused(t *testing.T) {
	src := filepath.Join(t.TempDir(), "f")
	writeFile(t, src, "x", 0o644)

	if err := copyPath(t, src, filepath.Join(t.TempDir(), "missing", "f")); err == nil {
		t.Error("copied into a directory that does not exist")
	}
}

// A read-only directory still gets its contents: its mode is applied after.
func TestCopyReadOnlyDirectory(t *testing.T) {
	src := filepath.Join(t.TempDir(), "ro")
	writeFile(t, filepath.Join(src, "f"), "x", 0o444)
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o755) })

	dest := t.TempDir()
	if err := copyPath(t, src, dest); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dest, "ro"), 0o755) })

	info, err := os.Stat(filepath.Join(dest, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o555 {
		t.Errorf("mode = %o, want 555", info.Mode().Perm())
	}
}

// An existing directory the archive lands in keeps its own mode.
func TestCopyLeavesExistingDirectoriesAlone(t *testing.T) {
	src := filepath.Join(t.TempDir(), "etc")
	writeFile(t, filepath.Join(src, "new.conf"), "x", 0o644)
	if err := os.Chmod(src, 0o700); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := os.Mkdir(filepath.Join(dest, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyPath(t, src, dest); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dest, "etc"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("existing directory mode = %o, want it left at 755", info.Mode().Perm())
	}
}

// hostileArchive builds an archive entry by entry, as a hostile guest could.
func hostileArchive(t *testing.T, entries ...*tar.Header) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, hdr := range entries {
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = 4
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte("evil")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return &buf
}

// Nothing an archive says can put a file outside the destination.
func TestHostileArchivesStayInside(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []*tar.Header
	}{
		{"parent traversal", []*tar.Header{
			{Name: "x/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "x/../../outside", Typeflag: tar.TypeReg, Mode: 0o644},
		}},
		{"absolute path", []*tar.Header{
			{Name: "x/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "/outside", Typeflag: tar.TypeReg, Mode: 0o644},
		}},
		{"second top-level entry", []*tar.Header{
			{Name: "x", Typeflag: tar.TypeReg, Mode: 0o644},
			{Name: "outside", Typeflag: tar.TypeReg, Mode: 0o644},
		}},
		{"write through a symlink", []*tar.Header{
			{Name: "x/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "x/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside-dir"},
			{Name: "x/link/outside", Typeflag: tar.TypeReg, Mode: 0o644},
		}},
		{"absolute symlink", []*tar.Header{
			{Name: "x/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "x/link", Typeflag: tar.TypeSymlink, Linkname: "/"},
			{Name: "x/link/outside", Typeflag: tar.TypeReg, Mode: 0o644},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, "in", "dest")
			outsideDir := filepath.Join(base, "in", "outside-dir")
			for _, dir := range []string{dest, outsideDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			if err := Unpack(hostileArchive(t, tc.entries...), dest); err == nil {
				t.Error("a hostile archive unpacked without error")
			}

			for _, path := range []string{
				filepath.Join(base, "outside"),
				filepath.Join(base, "in", "outside"),
				filepath.Join(outsideDir, "outside"),
				"/outside",
			} {
				if _, err := os.Lstat(path); err == nil {
					t.Errorf("%s was written outside the destination", path)
				}
			}
		})
	}
}

func TestUnpackEmptyArchive(t *testing.T) {
	if err := Unpack(hostileArchive(t), t.TempDir()); err == nil {
		t.Error("an empty archive unpacked without error")
	}
}

func TestPackMissingSource(t *testing.T) {
	var buf bytes.Buffer
	if err := Pack(&buf, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("packed a path that does not exist")
	}
}

// Send and Receive carry an archive in bounded chunks, as a stream does.
func TestSendReceiveInChunks(t *testing.T) {
	src := filepath.Join(t.TempDir(), "big")
	writeFile(t, src, string(bytes.Repeat([]byte("0123456789"), ChunkSize/4)), 0o644)

	var chunks [][]byte
	sent, err := Send(src, func(chunk []byte) error {
		if len(chunk) > ChunkSize {
			t.Errorf("chunk of %d bytes, want at most %d", len(chunk), ChunkSize)
		}
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(chunks) < 2 || sent <= int64(ChunkSize) {
		t.Errorf("sent %d bytes in %d chunks, want it split", sent, len(chunks))
	}

	dest := t.TempDir()
	next := 0
	err = Receive(func() ([]byte, error) {
		if next == len(chunks) {
			return nil, io.EOF
		}
		next++
		return chunks[next-1], nil
	}, dest)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	if readFile(t, filepath.Join(dest, "big")) != readFile(t, src) {
		t.Error("the file arrived changed")
	}
}

// Like cp, the path given is followed if it is a symlink: copying a link
// copies what it points to, under the link's name.
func TestCopySymlinkedSourceFollowsIt(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "usr", "os-release"), "ID=alpine\n", 0o644)
	if err := os.Symlink("usr/os-release", filepath.Join(base, "os-release")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("usr", filepath.Join(base, "lib")); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()

	if err := copyPath(t, filepath.Join(base, "os-release"), dest); err != nil {
		t.Fatalf("copy a link to a file: %v", err)
	}
	info, err := os.Lstat(filepath.Join(dest, "os-release"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("os-release = %v (%v), want a regular file", info, err)
	}
	if got := readFile(t, filepath.Join(dest, "os-release")); got != "ID=alpine\n" {
		t.Errorf("os-release = %q, want the target's contents", got)
	}

	if err := copyPath(t, filepath.Join(base, "lib"), dest); err != nil {
		t.Fatalf("copy a link to a directory: %v", err)
	}
	if got := readFile(t, filepath.Join(dest, "lib", "os-release")); got != "ID=alpine\n" {
		t.Errorf("lib/os-release = %q, want the directory's contents under the link's name", got)
	}
}
