// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package kernel keeps guest kernels on the host: the default kernel this
// binary carries, and those clients import, checked against their SHA-256.
package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/types"
)

// Config configures a Manager.
type Config struct {
	// DataDir holds the kernels, under kernels/<id>; it is required.
	DataDir string
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// Manager keeps kernel binaries on local disk. It is safe for concurrent
// use.
type Manager struct {
	dataDir string
	logger  *slog.Logger
}

// NewManager returns a Manager for the kernels under cfg.DataDir.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Manager{
		dataDir: cfg.DataDir,
		logger:  cfg.Logger.With("component", "kernel"),
	}, nil
}

// dir returns the directory holding the binary of the kernel with the given
// ID.
func (m *Manager) dir(id string) string {
	return filepath.Join(m.dataDir, "kernels", id)
}

// binaryPath returns the path of the binary of the kernel with the given ID.
func (m *Manager) binaryPath(id string) string {
	return filepath.Join(m.dir(id), "vmlinux")
}

// Path returns the local path to a kernel's binary. It returns an
// errdefs.ErrInvalidState error if the binary is missing from the host, or
// does not match the kernel's checksum.
func (m *Manager) Path(k types.Kernel) (string, error) {
	path := m.binaryPath(k.ID)

	remedy := "delete it and import it again"
	if k.Name == types.DefaultKernelName {
		remedy = "restart the daemon, which puts it back"
	}

	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", errdefs.InvalidState("kernel %q is missing from the host: %s", k.Name, remedy)
	} else if err != nil {
		return "", fmt.Errorf("find kernel %q: %w", k.Name, err)
	}

	// A kernel imported before every import recorded its SHA-256 has none
	// to check it by.
	if k.SHA256 == "" {
		return "", errdefs.InvalidState("kernel %q has no checksum to check it by: %s", k.Name, remedy)
	}
	got, err := fileSHA256(path)
	if err != nil {
		return "", fmt.Errorf("checksum kernel %q: %w", k.Name, err)
	}
	if got != k.SHA256 {
		return "", errdefs.InvalidState("kernel %q on the host does not match its checksum: %s", k.Name, remedy)
	}
	return path, nil
}

// ExtractDefault puts the default kernel this binary carries on the host, as
// the binary of the kernel with the given ID, in place of whatever is there.
func (m *Manager) ExtractDefault(id string) error {
	if err := m.Delete(id); err != nil {
		return fmt.Errorf("remove the previous default kernel: %w", err)
	}
	if _, err := Extract(m.binaryPath(id), DefaultVersion); err != nil {
		return fmt.Errorf("extract the default kernel: %w", err)
	}
	m.logger.Info("default kernel extracted", "version", DefaultVersion)
	return nil
}

// MaxImportBytes is the largest kernel a client can import.
const MaxImportBytes = 512 << 20

// Import keeps the kernel a client sends, read from r, as the binary of the
// kernel with the given ID, and returns its hex-encoded SHA-256. It refuses
// an empty kernel, one larger than MaxImportBytes, or one whose SHA-256 is
// not wantSHA256 if that is set. Nothing is kept if it fails.
func (m *Manager) Import(id string, r io.Reader, wantSHA256 string) (string, error) {
	path := m.binaryPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create kernel directory: %w", err)
	}

	partial := path + ".partial"
	defer func() { _ = os.Remove(partial) }()

	hash := sha256.New()
	n, err := createFile(partial, io.TeeReader(io.LimitReader(r, MaxImportBytes+1), hash))
	switch {
	case err != nil:
		return "", err
	case n == 0:
		return "", errdefs.InvalidArgument("the kernel is empty")
	case n > MaxImportBytes:
		return "", errdefs.InvalidArgument("the kernel is larger than %s", humanize.Bytes(MaxImportBytes))
	}

	digest := hex.EncodeToString(hash.Sum(nil))
	if wantSHA256 != "" && digest != wantSHA256 {
		return "", errdefs.InvalidArgument("the kernel's SHA-256 is %s, not %s", digest, wantSHA256)
	}

	if err := os.Rename(partial, path); err != nil {
		return "", fmt.Errorf("install kernel: %w", err)
	}
	return digest, nil
}

// DiskBytes returns the size of a kernel's binary on local disk, or 0 if it
// is not there.
func (m *Manager) DiskBytes(id string) int64 {
	info, err := os.Stat(m.binaryPath(id))
	if err != nil {
		return 0
	}

	return info.Size()
}

// Delete removes a kernel's binary from local disk. Deleting one that is not
// on disk is not an error.
func (m *Manager) Delete(id string) error {
	return os.RemoveAll(m.dir(id))
}

// fileSHA256 returns the lower-hex SHA-256 digest of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// createFile streams src into a new executable file at dst, synced to disk,
// and returns the bytes it wrote, even on failure. On failure it removes dst.
func createFile(dst string, src io.Reader) (n int64, err error) {
	f, err := os.Create(dst)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}

	defer func() {
		closeErr := f.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("close %s: %w", dst, closeErr)
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()

	if n, err = io.Copy(f, src); err != nil {
		return n, fmt.Errorf("write file: %w", err)
	}
	if err := f.Chmod(0o755); err != nil {
		return n, fmt.Errorf("chmod: %w", err)
	}
	// Flushed before it is renamed into place, so that a crash cannot leave
	// a renamed file whose contents never reached the disk.
	if err := f.Sync(); err != nil {
		return n, fmt.Errorf("sync: %w", err)
	}

	return n, nil
}
