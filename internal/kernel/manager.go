// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package kernel downloads and caches guest kernels, verifying their SHA-256
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

	"github.com/docker/go-units"
	"golang.org/x/sync/singleflight"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
)

// Config configures a Manager.
type Config struct {
	DataDir string
	Metrics Metrics
	Events  Events
	Logger  *slog.Logger
}

// Events records the kernels fetched. It is declared here, and satisfied by
// internal/events, so that this package reports what it does without knowing
// who listens.
type Events interface {
	Record(e events.Event)
}

// discardEvents is the Events used when none is configured.
type discardEvents struct{}

func (discardEvents) Record(events.Event) {}

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
type Manager struct {
	dataDir   string
	fetchFunc fetchFunc
	fetches   singleflight.Group // one download per kernel at a time
	metrics   Metrics
	events    Events
	logger    *slog.Logger
}

// NewManager creates a kernel Manager.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if cfg.Metrics == nil {
		cfg.Metrics = discardMetrics{}
	}
	if cfg.Events == nil {
		cfg.Events = discardEvents{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	s := &Manager{
		dataDir:   cfg.DataDir,
		fetchFunc: fetchURL,
		metrics:   cfg.Metrics,
		events:    cfg.Events,
		logger:    cfg.Logger.With("component", "kernel"),
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

// Path returns the local path to a kernel's binary, downloading it if it is
// missing or its checksum does not match.
func (m *Manager) Path(ctx context.Context, k types.Kernel) (string, error) {
	localPath := m.kernelPath(k.ID)

	if m.cached(localPath, k.SHA256) {
		return localPath, nil
	}

	if k.URL == "" {
		return "", fmt.Errorf("kernel %q has no URL to fetch from", k.Name)
	}

	// Concurrent callers share one download, bounded by fetchTimeout rather
	// than any caller's cancellation.
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
func (m *Manager) fetch(ctx context.Context, k types.Kernel, path string) (err error) {
	var fetched int64
	started := time.Now()
	defer func() { m.metrics.RecordKernelFetch(err, time.Since(started), fetched) }()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create kernel dir: %w", err)
	}

	m.logger.InfoContext(ctx, "downloading kernel", "kernel", k.Name, "url", k.URL)

	tmp := path + ".download"
	defer func() { _ = os.Remove(tmp) }()

	if fetched, err = m.fetchFunc(ctx, k.URL, tmp); err != nil {
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
		Message: fmt.Sprintf("Fetched kernel from %s in %s: %s, %s", k.URL, d.Round(time.Millisecond),
			units.CustomSize("%.4g %s", float64(fetchedBytes), 1024, []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}),
			verified),
		Attributes: map[string]string{"url": k.URL, "fetched_bytes": strconv.FormatInt(fetchedBytes, 10)},
	})
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

// DiskBytes returns the size of a kernel's binary on local disk, or 0 if it
// has not been downloaded.
func (m *Manager) DiskBytes(id string) int64 {
	info, err := os.Stat(m.kernelPath(id))
	if err != nil {
		return 0
	}

	return info.Size()
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
func fetchURL(ctx context.Context, url, dst string) (int64, error) {
	if local := localSource(url); local != "" {
		return copyLocal(local, dst)
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

func copyLocal(src, dst string) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("open local file: %w", err)
	}
	defer func() { _ = f.Close() }()

	return writeTo(dst, f)
}

// writeTo streams src into a new file at dst, checking the Close error, and
// returns the bytes it wrote, even on failure.
func writeTo(dst string, src io.Reader) (n int64, err error) {
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
