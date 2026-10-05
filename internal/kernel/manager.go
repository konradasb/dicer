// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package kernel fetches and caches guest kernels, verifying their SHA-256
// when given.
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
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/types"
)

// Config configures a Manager.
type Config struct {
	// DataDir holds the kernels, under kernels/<id>; it is required.
	DataDir string
	// Metrics and Events default to discarding what they are given.
	Metrics Metrics
	Events  Recorder
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// Recorder records the kernels fetched. It is declared here, and satisfied by
// internal/events, so that this package reports what it does without knowing
// who listens.
type Recorder interface {
	Record(e events.Event)
}

// discardRecorder is the Recorder used when none is configured.
type discardRecorder struct{}

func (discardRecorder) Record(events.Event) {}

// Metrics records how fetches went. It is declared here, and satisfied by
// internal/metrics, so this package measures itself without depending on a
// metrics library.
type Metrics interface {
	// RecordKernelFetch records a kernel downloaded or copied into place:
	// its outcome, how long it took and the bytes it fetched.
	RecordKernelFetch(err error, d time.Duration, fetchedBytes int64)
}

// discardMetrics is the Metrics used when none is configured.
type discardMetrics struct{}

func (discardMetrics) RecordKernelFetch(error, time.Duration, int64) {}

// fetchFunc fetches url into a new file at dst and returns the bytes it
// wrote, even on failure.
type fetchFunc func(ctx context.Context, url, dst string) (int64, error)

// Manager caches kernel binaries on local disk, fetching each on first use.
// It is safe for concurrent use.
type Manager struct {
	dataDir   string
	fetchFunc fetchFunc
	fetches   singleflight.Group // one fetch per kernel at a time
	metrics   Metrics
	events    Recorder
	logger    *slog.Logger
}

// NewManager returns a Manager for the kernels under cfg.DataDir.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if cfg.Metrics == nil {
		cfg.Metrics = discardMetrics{}
	}
	if cfg.Events == nil {
		cfg.Events = discardRecorder{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Manager{
		dataDir:   cfg.DataDir,
		fetchFunc: fetchURL,
		metrics:   cfg.Metrics,
		events:    cfg.Events,
		logger:    cfg.Logger.With("component", "kernel"),
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

// Path returns the local path to a kernel's binary, fetching it if it is
// missing or its checksum does not match. Concurrent callers share one
// fetch, which goes on when ctx is cancelled so that the others still get
// the kernel.
func (m *Manager) Path(ctx context.Context, k types.Kernel) (string, error) {
	path := m.binaryPath(k.ID)

	if m.cached(path, k.SHA256) {
		return path, nil
	}

	if k.URL == "" {
		return "", fmt.Errorf("kernel %q has no URL to fetch from", k.Name)
	}

	// Concurrent callers share one fetch, bounded by fetchTimeout rather
	// than any caller's cancellation.
	results := m.fetches.DoChan(k.ID, func() (any, error) {
		if m.cached(path, k.SHA256) {
			return path, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()
		return path, m.fetch(fetchCtx, k, path)
	})

	select {
	case r := <-results:
		if r.Err != nil {
			return "", r.Err
		}
		return path, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// fetchTimeout bounds a kernel fetch.
const fetchTimeout = 10 * time.Minute

// fetch fetches a kernel beside path, verifies it and only then renames it
// into place, so that path never holds a partial or unverified kernel: a copy
// without a checksum to check it against is trusted as it is.
func (m *Manager) fetch(ctx context.Context, k types.Kernel, path string) (err error) {
	var fetched int64
	started := time.Now()
	defer func() { m.metrics.RecordKernelFetch(err, time.Since(started), fetched) }()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create kernel directory: %w", err)
	}

	m.logger.InfoContext(ctx, "fetching kernel", "kernel", k.Name, "url", k.URL)

	partial := path + ".download"
	defer func() { _ = os.Remove(partial) }()

	if fetched, err = m.fetchFunc(ctx, k.URL, partial); err != nil {
		return fmt.Errorf("fetch kernel %q: %w", k.Name, err)
	}

	if k.SHA256 != "" {
		computed, err := fileSHA256(partial)
		if err != nil {
			return fmt.Errorf("checksum kernel %q: %w", k.Name, err)
		}
		if computed != k.SHA256 {
			return fmt.Errorf("kernel %q checksum mismatch: want %s, got %s", k.Name, k.SHA256, computed)
		}
	}

	if err := os.Rename(partial, path); err != nil {
		return fmt.Errorf("install kernel %q: %w", k.Name, err)
	}

	m.logger.InfoContext(ctx, "kernel ready", "kernel", k.Name, "path", path)
	m.recordFetched(k, time.Since(started), fetched)
	return nil
}

// recordFetched records that k was fetched, in d, as fetchedBytes.
func (m *Manager) recordFetched(k types.Kernel, d time.Duration, fetchedBytes int64) {
	verified := "no checksum to verify"
	if k.SHA256 != "" {
		verified = "checksum verified"
	}

	m.events.Record(events.Event{
		Kind:   events.KindKernel,
		ID:     k.ID,
		Name:   k.Name,
		Action: events.ActionFetched,
		Message: fmt.Sprintf("Fetched kernel from %s in %s: %s, %s",
			k.URL, humanize.Duration(d), humanize.Bytes(fetchedBytes), verified),
		Attributes: map[string]string{"url": k.URL, "fetched_bytes": strconv.FormatInt(fetchedBytes, 10)},
	})
}

// cached reports whether a usable copy is already on disk. An unverifiable or
// mismatched copy counts as absent.
func (m *Manager) cached(path, wantSHA256 string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	if wantSHA256 == "" {
		return true
	}

	got, err := fileSHA256(path)
	if err != nil {
		return false
	}
	if got != wantSHA256 {
		m.logger.Warn("cached kernel failed checksum, refetching", "path", path)
		return false
	}

	return true
}

// DiskBytes returns the size of a kernel's binary on local disk, or 0 if it
// has not been fetched.
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

// fetchURL downloads from an http(s) URL, or copies from a local path given
// as file:// or as an absolute path.
func fetchURL(ctx context.Context, url, dst string) (int64, error) {
	if local := localPath(url); local != "" {
		f, err := os.Open(local)
		if err != nil {
			return 0, fmt.Errorf("open local file: %w", err)
		}
		defer func() { _ = f.Close() }()
		return createFile(dst, f)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	return createFile(dst, resp.Body)
}

// localPath returns the filesystem path a URL refers to, or "" if it is not
// a local reference.
func localPath(url string) string {
	if after, ok := strings.CutPrefix(url, "file://"); ok {
		return after
	}
	if filepath.IsAbs(url) {
		return url
	}

	return ""
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
