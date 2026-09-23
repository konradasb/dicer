// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewClient(t *testing.T) {
	tmpDir := t.TempDir()

	client, err := NewClient(tmpDir)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if client == nil {
		t.Fatal("NewClient() returned nil")
	}

	// Verify cache dir was created
	if _, err := os.Stat(tmpDir); err != nil {
		t.Errorf("cache dir not created: %v", err)
	}
}

func TestNewClient_InvalidCacheDir(t *testing.T) {
	// A directory cannot be created beneath a regular file, whoever the test
	// runs as -- unlike a path under /nonexistent, which root can create.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewClient(filepath.Join(file, "data")); err == nil {
		t.Error("NewClient() should fail with invalid cache dir")
	}
}

func TestClient_Resolve(t *testing.T) {
	// This is an integration test that requires network access
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	client, err := NewClient(tmpDir)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx := context.Background()

	// Test with a small, stable image
	digest, err := client.inspectManifest(ctx, "alpine:3.18")
	if err != nil {
		t.Fatalf("inspectManifest() error = %v", err)
	}

	if digest == "" {
		t.Error("inspectManifest() returned empty digest")
	}

	// Verify digest format
	if len(digest) < 10 || digest[:7] != "sha256:" {
		t.Errorf("digest format invalid: %s", digest)
	}
}

func TestClient_PullAndExport(t *testing.T) {
	// This is an integration test that requires network access and takes time
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")
	exportDir := filepath.Join(tmpDir, "export")

	client, err := NewClient(cacheDir)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx := context.Background()

	// First resolve the digest
	digest, err := client.inspectManifest(ctx, "alpine:3.18")
	if err != nil {
		t.Fatalf("inspectManifest() error = %v", err)
	}

	// Pull and export
	result, err := client.PullAndExport(ctx, "alpine:3.18", digest, exportDir, nil)
	if err != nil {
		t.Fatalf("PullAndExport() error = %v", err)
	}

	if result == nil {
		t.Fatal("PullAndExport() returned nil result")
	}

	if result.Digest != digest {
		t.Errorf("result.Digest = %v, want %v", result.Digest, digest)
	}

	if result.Metadata == nil {
		t.Fatal("result.Metadata is nil")
	}

	// Verify export directory exists and has content
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}

	if len(entries) == 0 {
		t.Error("export directory is empty")
	}

	t.Logf("Exported %d entries", len(entries))
	t.Logf("Metadata: Entrypoint=%v, Cmd=%v, WorkingDir=%s",
		result.Metadata.Entrypoint, result.Metadata.Cmd, result.Metadata.WorkingDir)
}

func TestClient_PullAndExport_Cached(t *testing.T) {
	// Test that second pull uses cache
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")
	exportDir1 := filepath.Join(tmpDir, "export1")
	exportDir2 := filepath.Join(tmpDir, "export2")

	client, err := NewClient(cacheDir)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx := context.Background()
	digest, err := client.inspectManifest(ctx, "alpine:3.18")
	if err != nil {
		t.Fatalf("inspectManifest() error = %v", err)
	}

	// First pull
	_, err = client.PullAndExport(ctx, "alpine:3.18", digest, exportDir1, nil)
	if err != nil {
		t.Fatalf("first PullAndExport() error = %v", err)
	}

	// Second pull (should use cache)
	_, err = client.PullAndExport(ctx, "alpine:3.18", digest, exportDir2, nil)
	if err != nil {
		t.Fatalf("second PullAndExport() error = %v", err)
	}

	// Both exports should have content
	entries1, _ := os.ReadDir(exportDir1)
	entries2, _ := os.ReadDir(exportDir2)

	if len(entries1) == 0 || len(entries2) == 0 {
		t.Error("export directories should not be empty")
	}
}

func TestClient_PullAndExport_EmptyDigest(t *testing.T) {
	tmpDir := t.TempDir()
	client, _ := NewClient(tmpDir)

	ctx := context.Background()
	_, err := client.PullAndExport(ctx, "alpine:latest", "", tmpDir, nil)
	if err == nil {
		t.Error("PullAndExport() should fail with empty digest")
	}
}

func TestClient_PullAndExport_EmptyExportDir(t *testing.T) {
	tmpDir := t.TempDir()
	client, _ := NewClient(tmpDir)

	ctx := context.Background()
	_, err := client.PullAndExport(ctx, "alpine:latest", "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", "", nil)
	if err == nil {
		t.Error("PullAndExport() should fail with empty export dir")
	}
}

func TestClient_Metadata(t *testing.T) {
	// This requires a cached image
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")
	exportDir := filepath.Join(tmpDir, "export")

	client, err := NewClient(cacheDir)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx := context.Background()

	// First pull an image
	digest, err := client.inspectManifest(ctx, "alpine:3.18")
	if err != nil {
		t.Fatalf("inspectManifest() error = %v", err)
	}

	_, err = client.PullAndExport(ctx, "alpine:3.18", digest, exportDir, nil)
	if err != nil {
		t.Fatalf("PullAndExport() error = %v", err)
	}

	// Now test metadata
	layoutTag, _ := digestToLayoutTag(digest)
	meta, err := client.metadata(layoutTag)
	if err != nil {
		t.Fatalf("metadata() error = %v", err)
	}

	if meta == nil {
		t.Fatal("metadata() returned nil")
	}

	// Alpine should have /bin/sh as entrypoint or cmd
	t.Logf("Metadata: Entrypoint=%v, Cmd=%v, Env=%v, WorkingDir=%s",
		meta.Entrypoint, meta.Cmd, meta.Env, meta.WorkingDir)
}

func TestDigestToLayoutTag(t *testing.T) {
	tests := []struct {
		name    string
		digest  string
		want    string
		wantErr bool
	}{
		{
			name:   "valid sha256",
			digest: "sha256:abc123",
			want:   "abc123",
		},
		{
			name:   "valid sha512",
			digest: "sha512:def456",
			want:   "def456",
		},
		{
			name:    "missing colon",
			digest:  "sha256abc123",
			wantErr: true,
		},
		{
			name:    "empty after colon",
			digest:  "sha256:",
			wantErr: true,
		},
		{
			name:    "empty string",
			digest:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := digestToLayoutTag(tt.digest)
			if tt.wantErr {
				if err == nil {
					t.Error("digestToLayoutTag() should return error")
				}
				return
			}

			if err != nil {
				t.Fatalf("digestToLayoutTag() error = %v", err)
			}

			if got != tt.want {
				t.Errorf("digestToLayoutTag() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		envList []string
		want    map[string]string
	}{
		{
			name:    "empty",
			envList: []string{},
			want:    map[string]string{},
		},
		{
			name: "single var",
			envList: []string{
				"PATH=/usr/bin",
			},
			want: map[string]string{
				"PATH": "/usr/bin",
			},
		},
		{
			name: "multiple vars",
			envList: []string{
				"PATH=/usr/bin:/bin",
				"HOME=/root",
				"USER=root",
			},
			want: map[string]string{
				"PATH": "/usr/bin:/bin",
				"HOME": "/root",
				"USER": "root",
			},
		},
		{
			name: "value with equals",
			envList: []string{
				"DOCKER_CONFIG={\"auths\":{}}",
			},
			want: map[string]string{
				"DOCKER_CONFIG": "{\"auths\":{}}",
			},
		},
		{
			name: "empty value",
			envList: []string{
				"EMPTY=",
			},
			want: map[string]string{
				"EMPTY": "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEnvVars(tt.envList)

			if len(got) != len(tt.want) {
				t.Errorf("len(parseEnvVars()) = %d, want %d", len(got), len(tt.want))
			}

			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseEnvVars()[%s] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}
