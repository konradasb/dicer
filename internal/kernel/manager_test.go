// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

func newTestManager(t *testing.T, fetch func(ctx context.Context, url, dst string) error) *Manager {
	t.Helper()

	c, err := NewManager(Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c.fetchFunc = fetch

	return c
}

// writeFetcher returns a fetch func that writes fixed contents and counts calls.
func writeFetcher(contents string, calls *int) func(context.Context, string, string) error {
	return func(_ context.Context, _, dst string) error {
		*calls++
		return os.WriteFile(dst, []byte(contents), 0o755)
	}
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestPathFetchesOnceAndThenCaches is the behaviour the Store exists for:
// Path is called on every instance start, so it must not hit the network
// every time.
func TestPathFetchesOnceAndThenCaches(t *testing.T) {
	var calls int
	c := newTestManager(t, writeFetcher("vmlinux", &calls))
	k := types.Kernel{ID: "k1", Name: "test", URL: "https://example.invalid/vmlinux"}

	first, err := c.Path(context.Background(), k)
	if err != nil {
		t.Fatalf("first Path: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetches after first call = %d, want 1", calls)
	}

	second, err := c.Path(context.Background(), k)
	if err != nil {
		t.Fatalf("second Path: %v", err)
	}
	if second != first {
		t.Errorf("path changed between calls: %q then %q", first, second)
	}
	if calls != 1 {
		t.Errorf("fetches after second call = %d, want 1 (the copy on disk should be reused)", calls)
	}
}

func TestPathVerifiesChecksum(t *testing.T) {
	var calls int
	c := newTestManager(t, writeFetcher("wrong contents", &calls))
	k := types.Kernel{ID: "k1", Name: "test", URL: "https://example.invalid/vmlinux", SHA256: sha256Of("vmlinux")}

	if _, err := c.Path(context.Background(), k); err == nil {
		t.Fatal("Path should fail when the download does not match the expected checksum")
	}

	// A failed fetch must leave nothing behind for the next call to adopt.
	if _, err := os.Stat(c.kernelPath(k.ID)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("failed download left a file on disk")
	}
}

// TestPathRefetchesCorruptedCache covers a cached copy that no longer matches
// its checksum -- a truncated download must not be served forever.
func TestPathRefetchesCorruptedCache(t *testing.T) {
	var calls int
	c := newTestManager(t, writeFetcher("vmlinux", &calls))
	k := types.Kernel{ID: "k1", Name: "test", URL: "https://example.invalid/vmlinux", SHA256: sha256Of("vmlinux")}

	path, err := c.Path(context.Background(), k)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetches = %d, want 1", calls)
	}

	if err := os.WriteFile(path, []byte("corrupted"), 0o755); err != nil {
		t.Fatalf("corrupt the cached file: %v", err)
	}

	if _, err := c.Path(context.Background(), k); err != nil {
		t.Fatalf("Path after corruption: %v", err)
	}
	if calls != 2 {
		t.Errorf("fetches = %d, want 2 (the corrupted copy should be refetched)", calls)
	}
}

func TestPathWithoutURL(t *testing.T) {
	c := newTestManager(t, func(context.Context, string, string) error {
		t.Fatal("should not fetch without a URL")
		return nil
	})

	if _, err := c.Path(context.Background(), types.Kernel{ID: "k1", Name: "test"}); err == nil {
		t.Error("Path should fail for a kernel with no URL and nothing cached")
	}
}

// TestPathWithoutChecksumUsesCache covers kernels imported without a SHA256:
// there is nothing to verify, so an existing copy is trusted.
func TestPathWithoutChecksumUsesCache(t *testing.T) {
	var calls int
	c := newTestManager(t, writeFetcher("vmlinux", &calls))
	k := types.Kernel{ID: "k1", Name: "test", URL: "https://example.invalid/vmlinux"}

	for range 3 {
		if _, err := c.Path(context.Background(), k); err != nil {
			t.Fatalf("Path: %v", err)
		}
	}
	if calls != 1 {
		t.Errorf("fetches = %d, want 1", calls)
	}
}

func TestDelete(t *testing.T) {
	var calls int
	c := newTestManager(t, writeFetcher("vmlinux", &calls))
	k := types.Kernel{ID: "k1", Name: "test", URL: "https://example.invalid/vmlinux"}

	path, err := c.Path(context.Background(), k)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}

	if err := c.Delete(k.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("kernel directory still present after Delete")
	}

	// Deleting again is not an error.
	if err := c.Delete(k.ID); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

// TestConcurrentPathsShareOneDownload covers instances starting at once with
// a kernel not yet fetched: they share one download, and none of them sees
// the kernel before it is whole.
func TestConcurrentPathsShareOneDownload(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	c := newTestManager(t, func(_ context.Context, _, dst string) error {
		calls.Add(1)
		<-release
		return os.WriteFile(dst, []byte("vmlinux"), 0o755)
	})
	k := types.Kernel{ID: "k1", Name: "test", URL: "https://example.invalid/vmlinux"}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			path, err := c.Path(t.Context(), k)
			if err != nil {
				t.Errorf("Path: %v", err)
				return
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "vmlinux" {
				t.Errorf("Path returned %q holding %q, %v; want the whole kernel", path, data, err)
			}
		})
	}

	// Give every caller the chance to find the download in progress.
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(c.kernelPath(k.ID)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the kernel is in place before its download finished")
	}
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("downloads = %d, want 1", n)
	}
}
