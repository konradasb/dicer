// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()

	m, err := NewManager(Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// importKernel imports contents as the kernel with the given ID and name, and
// returns it.
func importKernel(t *testing.T, m *Manager, id, name, contents string) types.Kernel {
	t.Helper()

	digest, err := m.Import(id, strings.NewReader(contents), "")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return types.Kernel{ID: id, Name: name, SHA256: digest}
}

// TestImportKeepsAKernelWithItsChecksum checks that an imported kernel is
// kept as the binary instances boot, with the SHA-256 of what was sent, and
// that one that fails a checksum it was given, or is empty, is not kept.
func TestImportKeepsAKernelWithItsChecksum(t *testing.T) {
	m := newTestManager(t)

	k := importKernel(t, m, "k1", "test", "vmlinux")
	if k.SHA256 != sha256Of("vmlinux") {
		t.Errorf("Import returned %s, want the SHA-256 of what was sent", k.SHA256)
	}
	path, err := m.Path(k)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "vmlinux" {
		t.Errorf("the kernel kept = %q, %v; want vmlinux", data, err)
	}

	for _, tt := range []struct {
		name, contents, sha256 string
	}{
		{"a checksum it fails", "vmlinux", sha256Of("other")},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := m.Import("bad", strings.NewReader(tt.contents), tt.sha256); !errors.Is(err, errdefs.ErrInvalidArgument) {
				t.Errorf("Import = %v, want errdefs.ErrInvalidArgument", err)
			}
			if _, err := os.Stat(m.binaryPath("bad")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a refused kernel was kept: %v", err)
			}
		})
	}
}

// TestPathRefusesAMissingOrDamagedKernel checks that a kernel whose binary
// has gone from the host, no longer matches its checksum, or has none, is
// refused, saying so and what to do.
func TestPathRefusesAMissingOrDamagedKernel(t *testing.T) {
	m := newTestManager(t)

	tests := []struct {
		name   string
		kernel func() types.Kernel
		want   string
	}{
		{
			"missing",
			func() types.Kernel {
				k := importKernel(t, m, "k1", "gone", "vmlinux")
				_ = os.Remove(m.binaryPath(k.ID))
				return k
			},
			`kernel "gone" is missing from the host: delete it and import it again`,
		},
		{
			"damaged",
			func() types.Kernel {
				k := importKernel(t, m, "k2", "bad", "vmlinux")
				_ = os.WriteFile(m.binaryPath(k.ID), []byte("damaged"), 0o755)
				return k
			},
			`kernel "bad" on the host does not match its checksum: delete it and import it again`,
		},
		{
			"with no checksum",
			func() types.Kernel {
				k := importKernel(t, m, "k3", "old", "vmlinux")
				k.SHA256 = ""
				return k
			},
			`kernel "old" has no checksum to check it by: delete it and import it again`,
		},
		{
			"the default kernel, missing",
			func() types.Kernel {
				k := importKernel(t, m, "k4", types.DefaultKernelName, "vmlinux")
				_ = os.Remove(m.binaryPath(k.ID))
				return k
			},
			`kernel "default" is missing from the host: restart the daemon, which puts it back`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := m.Path(tt.kernel())
			if !errors.Is(err, errdefs.ErrInvalidState) || err.Error() != tt.want {
				t.Errorf("Path = %v, want errdefs.ErrInvalidState: %s", err, tt.want)
			}
		})
	}
}

// TestExtractDefaultReplacesWhatIsThere checks that the default kernel is
// extracted as a kernel's binary, in place of whatever was there.
func TestExtractDefaultReplacesWhatIsThere(t *testing.T) {
	m := newTestManager(t)
	importKernel(t, m, "k1", types.DefaultKernelName, "an older default kernel")

	if err := m.ExtractDefault("k1"); err != nil {
		t.Fatalf("ExtractDefault: %v", err)
	}

	k := Default()
	k.ID = "k1"
	if _, err := m.Path(k); err != nil {
		t.Errorf("Path of the extracted default kernel: %v", err)
	}
}

func TestDiskBytesIsSizeOfTheKernel(t *testing.T) {
	m := newTestManager(t)

	if got := m.DiskBytes("k1"); got != 0 {
		t.Errorf("DiskBytes before import = %d, want 0", got)
	}
	importKernel(t, m, "k1", "test", "vmlinux")
	if got := m.DiskBytes("k1"); got != 7 {
		t.Errorf("DiskBytes after import = %d, want 7", got)
	}
}

func TestDeleteRemovesKernelDirectory(t *testing.T) {
	m := newTestManager(t)
	k := importKernel(t, m, "k1", "test", "vmlinux")

	if err := m.Delete(k.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(m.binaryPath(k.ID))); !errors.Is(err, fs.ErrNotExist) {
		t.Error("kernel directory still present after Delete")
	}

	// Deleting again is not an error.
	if err := m.Delete(k.ID); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}
