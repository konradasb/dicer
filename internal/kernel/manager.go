// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package kernel keeps the guest kernels instances boot.
//
// A kernel is recorded by URL and downloaded on first use, verified against
// its SHA-256 when one was given. What is kept is the kernel itself and where
// it came from, which is dicer.Kernel.
package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/dicer-sh/dicer"
)

// Config configures a Manager.
type Config struct {
	DataDir string
	Logger  *slog.Logger
}

// Manager holds kernel binaries on local disk, fetching each one the first
// time it is needed.
//
// It records no metadata: a dicer.Kernel definition lives in the definition store
// and is passed in. This package owns only the bytes, and they are
// replaceable -- a deleted kernel costs another download, not data.
type Manager struct {
	dataDir   string
	fetchFunc func(ctx context.Context, url, dst string) error
	fetches   singleflight.Group // one download per kernel at a time
	logger    *slog.Logger
}

// NewManager creates a kernel Manager.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	s := &Manager{
		dataDir:   cfg.DataDir,
		logger:    cfg.Logger.With("component", "kernel"),
		fetchFunc: fetchURL,
	}

	return s, nil
}

// kernelDir returns the directory for a specific kernel ID.
func (m *Manager) kernelDir(id string) string {
	return filepath.Join(m.dataDir, "kernels", id)
}

// kernelPath returns the full path to a kernel binary.
func (m *Manager) kernelPath(id string) string {
	return filepath.Join(m.kernelDir(id), "vmlinux")
}

// Path returns the local path to a kernel's binary, downloading it on first
// use. Subsequent calls for the same kernel return the cached copy without
// touching the network -- which matters because this is called on every
// instance start.
//
// A cached copy whose checksum no longer matches is discarded and refetched,
// so a truncated or corrupted download cannot be served forever.
func (m *Manager) Path(ctx context.Context, k dicer.Kernel) (string, error) {
	localPath := m.kernelPath(k.ID)

	if m.cached(localPath, k.SHA256) {
		return localPath, nil
	}

	if k.URL == "" {
		return "", fmt.Errorf("kernel %q has no URL to fetch from", k.Name)
	}

	// Starts that need the same kernel at once share one download, rather
	// than each writing over the others'. It belongs to none of them, so
	// one giving up does not fail the rest: it keeps ctx's values, not its
	// cancellation, and is bounded by fetchTimeout instead.
	results := m.fetches.DoChan(k.ID, func() (any, error) {
		if m.cached(localPath, k.SHA256) {
			return localPath, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()
		return localPath, m.fetch(fetchCtx, k, localPath)
	})

	select {
	case r := <-results:
		if r.Err != nil {
			return "", r.Err
		}
		return localPath, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// fetchTimeout bounds a kernel download.
const fetchTimeout = 10 * time.Minute

// fetch downloads a kernel beside path, verifies it and only then renames it
// into place, so that path never holds a partial or unverified kernel -- which
// a copy without a checksum to check it against would be trusted as.
func (m *Manager) fetch(ctx context.Context, k dicer.Kernel, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create kernel dir: %w", err)
	}

	m.logger.InfoContext(ctx, "downloading kernel", "kernel", k.Name, "url", k.URL)

	tmp := path + ".download"
	defer func() { _ = os.Remove(tmp) }()

	if err := m.fetchFunc(ctx, k.URL, tmp); err != nil {
		return fmt.Errorf("download kernel %q: %w", k.Name, err)
	}

	if k.SHA256 != "" {
		computed, err := computeSHA256(tmp)
		if err != nil {
			return fmt.Errorf("checksum kernel %q: %w", k.Name, err)
		}
		if computed != k.SHA256 {
			return fmt.Errorf("kernel %q checksum mismatch: want %s, got %s", k.Name, k.SHA256, computed)
		}
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install kernel %q: %w", k.Name, err)
	}

	m.logger.InfoContext(ctx, "kernel ready", "kernel", k.Name, "path", path)
	return nil
}

// cached reports whether a usable copy is already on disk. An unverifiable or
// mismatched copy counts as absent.
func (m *Manager) cached(path, wantSHA string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	if wantSHA == "" {
		return true
	}

	got, err := computeSHA256(path)
	if err != nil {
		return false
	}
	if got != wantSHA {
		m.logger.Warn("cached kernel failed checksum, refetching", "path", path)
		return false
	}

	return true
}

// Delete removes a kernel's binary from local disk.
func (m *Manager) Delete(id string) error {
	return os.RemoveAll(m.kernelDir(id))
}

// computeSHA256 returns the lower-hex SHA-256 digest of the file at path.
func computeSHA256(path string) (string, error) {
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

// fetchURL downloads from an http(s) URL, or copies from a local path given
// as file:// or as an absolute path.
func fetchURL(ctx context.Context, url, dst string) error {
	if local := localSource(url); local != "" {
		return copyLocal(local, dst)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	return writeTo(dst, resp.Body)
}

// localSource returns the filesystem path a URL refers to, or "" if it is not
// a local reference.
func localSource(url string) string {
	if after, ok := strings.CutPrefix(url, "file://"); ok {
		return after
	}
	if filepath.IsAbs(url) {
		return url
	}

	return ""
}

func copyLocal(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open local file: %w", err)
	}
	defer func() { _ = f.Close() }()

	return writeTo(dst, f)
}

// writeTo streams src into a new file at dst.
//
// Close is checked, not deferred and discarded: it is where buffered writes
// are flushed, so a full disk surfaces here and nowhere else. Dropping it
// would report a successful download of a truncated kernel.
func writeTo(dst string, src io.Reader) (err error) {
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
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

	if _, err := io.Copy(f, src); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if err := f.Chmod(0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	// Flushed before it is renamed into place, so that a crash cannot leave
	// a renamed file whose contents never reached the disk.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	return nil
}
