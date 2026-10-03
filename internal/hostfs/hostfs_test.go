// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hostfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathIn(t *testing.T) {
	if got := pathIn(false, "/tmp/site"); got != "/tmp/site" {
		t.Errorf("in the host's namespace, /tmp/site = %s, want it as it is", got)
	}
	if got := pathIn(true, "/tmp/site"); got != "/proc/1/root/tmp/site" {
		t.Errorf("in a namespace of its own, /tmp/site = %s, want it under PID 1's root", got)
	}
	if got := pathIn(true, "site"); got != "site" {
		t.Errorf("a relative path = %s, want it left alone", got)
	}
}

func TestErrorsNameThePathGiven(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, err := range []error{
		func() error { _, err := Stat(missing); return err }(),
		func() error { _, err := ReadFile(missing); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), missing) || strings.Contains(err.Error(), "/proc/1/root") {
			t.Errorf("error = %v, want it to name %s as given", err, missing)
		}
	}
}

func TestCheckDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CheckDir(dir); err != nil {
		t.Errorf("CheckDir(directory) = %v, want nil", err)
	}
	if err := CheckDir(file); !errors.Is(err, ErrNotDirectory) || !strings.Contains(err.Error(), file) {
		t.Errorf("CheckDir(file) = %v, want ErrNotDirectory naming it", err)
	}
	if err := CheckDir(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("CheckDir(missing) = %v, want fs.ErrNotExist", err)
	}
}

func TestSameMountNamespace(t *testing.T) {
	dir := t.TempDir()
	link := func(name, target string) string {
		path := filepath.Join(dir, name)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	one, alsoOne, two := link("a", "mnt:[4026531841]"), link("b", "mnt:[4026531841]"), link("c", "mnt:[4026532999]")

	if !sameMountNamespace(one, alsoOne) {
		t.Error("two links to one namespace were taken for two")
	}
	if sameMountNamespace(one, two) {
		t.Error("links to two namespaces were taken for one")
	}
	if !sameMountNamespace(one, filepath.Join(dir, "missing")) {
		t.Error("an unreadable link was taken for another namespace")
	}
}
