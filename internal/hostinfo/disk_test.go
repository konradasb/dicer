// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hostinfo

import "testing"

func TestReadDiskUsage(t *testing.T) {
	usage, err := ReadDiskUsage(t.TempDir())
	if err != nil {
		t.Fatalf("ReadDiskUsage: %v", err)
	}

	if usage.TotalBytes <= 0 || usage.FreeBytes < 0 || usage.FreeBytes > usage.TotalBytes {
		t.Errorf("usage = %+v, want a size and a free amount within it", usage)
	}
}

func TestReadDiskUsageMissingPath(t *testing.T) {
	if _, err := ReadDiskUsage("/does/not/exist"); err == nil {
		t.Error("expected an error for a missing path")
	}
}
