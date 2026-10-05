// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package registry

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/image/reference"
)

// testImage is a small, stable image the network tests pull.
const testImage = "alpine:3.18"

func TestNewClientMakesTheLayerCacheDirectory(t *testing.T) {
	dataDir := t.TempDir()

	if _, err := NewClient(dataDir); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "oci-cache")); err != nil {
		t.Errorf("the layer cache directory: %v", err)
	}
}

func TestNewClientFailsWhereNoDirectoryCanBeMade(t *testing.T) {
	// A directory cannot be created beneath a regular file, whoever the test
	// runs as -- unlike a path under /nonexistent, which root can create.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewClient(filepath.Join(file, "data")); err == nil {
		t.Error("NewClient succeeded beneath a regular file, want an error")
	}
}

// newTestClient returns a client with a fresh layer cache, and the digest
// testImage currently points to. It needs the network.
func newTestClient(t *testing.T) (*Client, string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping a test that needs the network in short mode")
	}

	c, err := NewClient(t.TempDir())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ref, err := reference.Parse(testImage)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := c.Resolve(t.Context(), ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	return c, digest
}

func TestResolveReturnsAManifestDigest(t *testing.T) {
	_, digest := newTestClient(t)

	if !strings.HasPrefix(digest, "sha256:") || len(digest) <= len("sha256:") {
		t.Errorf("Resolve = %q, want a sha256 digest", digest)
	}
}

func TestPullAndExportUnpacksTheImage(t *testing.T) {
	c, digest := newTestClient(t)
	exportDir := filepath.Join(t.TempDir(), "export")

	metadata, err := c.PullAndExport(t.Context(), testImage, digest, exportDir, nil)
	if err != nil {
		t.Fatalf("PullAndExport: %v", err)
	}
	if metadata == nil {
		t.Fatal("PullAndExport returned no metadata")
	}

	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("the export directory is empty")
	}
}

// TestPullAndExportUnpacksACachedImageAgain checks that an image already in
// the layer cache is unpacked again for a second pull.
func TestPullAndExportUnpacksACachedImageAgain(t *testing.T) {
	c, digest := newTestClient(t)

	for _, dir := range []string{"first", "second"} {
		exportDir := filepath.Join(t.TempDir(), dir)
		if _, err := c.PullAndExport(t.Context(), testImage, digest, exportDir, nil); err != nil {
			t.Fatalf("%s PullAndExport: %v", dir, err)
		}
		if entries, err := os.ReadDir(exportDir); err != nil || len(entries) == 0 {
			t.Errorf("the %s export directory is empty: %v", dir, err)
		}
	}
}

func TestPullAndExportRejectsMissingArguments(t *testing.T) {
	const digest = "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	tests := []struct {
		name      string
		digest    string
		exportDir string
	}{
		{"no digest", "", t.TempDir()},
		{"no export directory", digest, ""},
	}

	c, err := NewClient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := c.PullAndExport(t.Context(), "alpine:latest", tt.digest, tt.exportDir, nil); err == nil {
				t.Error("PullAndExport succeeded, want an error")
			}
		})
	}
}

func TestDigestHex(t *testing.T) {
	tests := []struct {
		name    string
		digest  string
		want    string
		wantErr bool
	}{
		{name: "sha256", digest: "sha256:abc123", want: "abc123"},
		{name: "sha512", digest: "sha512:def456", want: "def456"},
		{name: "no colon", digest: "sha256abc123", wantErr: true},
		{name: "nothing after the colon", digest: "sha256:", wantErr: true},
		{name: "empty", digest: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := digestHex(tt.digest)
			if tt.wantErr {
				if err == nil {
					t.Errorf("digestHex(%q) = %q, want an error", tt.digest, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("digestHex(%q): %v", tt.digest, err)
			}
			if got != tt.want {
				t.Errorf("digestHex(%q) = %q, want %q", tt.digest, got, tt.want)
			}
		})
	}
}

func TestParseEnvSplitsAtTheFirstEquals(t *testing.T) {
	tests := []struct {
		name string
		list []string
		want map[string]string
	}{
		{"empty", []string{}, map[string]string{}},
		{"one variable", []string{"PATH=/usr/bin"}, map[string]string{"PATH": "/usr/bin"}},
		{
			"several variables",
			[]string{"PATH=/usr/bin:/bin", "HOME=/root", "USER=root"},
			map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/root", "USER": "root"},
		},
		{
			"value with equals",
			[]string{`DOCKER_CONFIG={"auths":{}}`},
			map[string]string{"DOCKER_CONFIG": `{"auths":{}}`},
		},
		{"empty value", []string{"EMPTY="}, map[string]string{"EMPTY": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseEnv(tt.list); !maps.Equal(got, tt.want) {
				t.Errorf("parseEnv(%q) = %v, want %v", tt.list, got, tt.want)
			}
		})
	}
}
