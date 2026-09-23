// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEROFSConverter_Convert(t *testing.T) {
	// Check if mkfs.erofs is available
	if _, err := exec.LookPath("mkfs.erofs"); err != nil {
		t.Skip("mkfs.erofs not found, skipping erofs tests")
	}

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

	packer := erofs{}
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

	// Verify size is reasonable
	if size == 0 {
		t.Error("size is 0")
	}
}

func TestEROFSConverter_ConvertEmpty(t *testing.T) {
	if _, err := exec.LookPath("mkfs.erofs"); err != nil {
		t.Skip("mkfs.erofs not found")
	}

	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "empty")
	outputPath := filepath.Join(tmpDir, "disk.img")

	// Create empty directory
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}

	packer := erofs{}
	ctx := context.Background()

	// Convert empty dir should work
	size, err := packer.Pack(ctx, dir, outputPath)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if size == 0 {
		t.Error("size should be > 0 even for empty dir")
	}
}

func TestEROFSConverter_ConvertNonExistent(t *testing.T) {
	if _, err := exec.LookPath("mkfs.erofs"); err != nil {
		t.Skip("mkfs.erofs not found")
	}

	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "disk.img")

	packer := erofs{}
	ctx := context.Background()

	// Pack non-existent dir should fail
	_, err := packer.Pack(ctx, filepath.Join(tmpDir, "nonexistent"), outputPath)
	if err == nil {
		t.Error("Convert() should fail with non-existent dir")
	}
}

func TestEROFSConverter_ConvertCancellation(t *testing.T) {
	if _, err := exec.LookPath("mkfs.erofs"); err != nil {
		t.Skip("mkfs.erofs not found")
	}

	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "rootfs")
	outputPath := filepath.Join(tmpDir, "disk.img")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}

	packer := erofs{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Pack with cancelled context should fail
	_, err := packer.Pack(ctx, dir, outputPath)
	if err == nil {
		t.Error("Convert() should fail with cancelled context")
	}
}

func TestEROFSConverter_ConvertInvalidOutput(t *testing.T) {
	if _, err := exec.LookPath("mkfs.erofs"); err != nil {
		t.Skip("mkfs.erofs not found")
	}

	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "rootfs")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}

	packer := erofs{}
	ctx := context.Background()

	// Beneath a regular file, which no one -- root included -- can create
	// a directory in.
	file := filepath.Join(tmpDir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := packer.Pack(ctx, dir, filepath.Join(file, "dir", "disk.img"))
	if err == nil {
		t.Error("Pack() should fail with invalid output path")
	}
}
