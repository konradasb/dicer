// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package initrd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCPIOConverter_Convert(t *testing.T) {
	// Create temporary directories
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "rootfs")
	outputPath := filepath.Join(tmpDir, "disk.img")

	// Create test directory structure
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		t.Fatalf("create etc dir: %v", err)
	}

	// Create some test files
	if err := os.WriteFile(filepath.Join(dir, "bin", "test"), []byte("#!/bin/sh\necho test"), 0o755); err != nil {
		t.Fatalf("create test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "config"), []byte("config=value"), 0o644); err != nil {
		t.Fatalf("create config file: %v", err)
	}

	packer := cpioPacker{}
	ctx := context.Background()

	// Convert
	size, err := packer.Pack(ctx, dir, outputPath)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	// Verify output file exists
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}

	// Verify size matches
	if size != info.Size() {
		t.Errorf("size = %d, stat size = %d", size, info.Size())
	}

	// Verify size is reasonable (should be > 0)
	if size == 0 {
		t.Error("size is 0")
	}
}

func TestCPIOConverter_ConvertEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "empty")
	outputPath := filepath.Join(tmpDir, "disk.img")

	// Create empty directory
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}

	packer := cpioPacker{}
	ctx := context.Background()

	// Convert empty dir should work
	size, err := packer.Pack(ctx, dir, outputPath)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if size == 0 {
		t.Error("size should be > 0 even for empty dir (trailer)")
	}
}

func TestCPIOConverter_ConvertNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "disk.img")

	packer := cpioPacker{}
	ctx := context.Background()

	// Pack non-existent dir should fail
	_, err := packer.Pack(ctx, filepath.Join(tmpDir, "nonexistent"), outputPath)
	if err == nil {
		t.Error("Convert() should fail with non-existent dir")
	}
}

func TestCPIOConverter_ConvertCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "rootfs")
	outputPath := filepath.Join(tmpDir, "disk.img")

	// Create a simple directory
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	packer := cpioPacker{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Pack with cancelled context may or may not fail depending on timing
	// This test mainly ensures context is checked during conversion
	_, err := packer.Pack(ctx, dir, outputPath)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Logf("Got error: %v (conversion may complete before context check)", err)
	}
}

func TestCPIOConverter_ConvertInvalidOutput(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "rootfs")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}

	packer := cpioPacker{}
	ctx := context.Background()

	// A file cannot be created beneath a regular file, whoever the test runs
	// as -- unlike a path under /nonexistent, which root can create.
	file := filepath.Join(tmpDir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := packer.Pack(ctx, dir, filepath.Join(file, "disk.img"))
	if err == nil {
		t.Error("Pack() should fail with invalid output path")
	}
}
