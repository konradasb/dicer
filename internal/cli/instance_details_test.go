// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"slices"
	"testing"

	"github.com/konradasb/dicer"
)

// TestMachineLineShowsTheMaximums checks that an instance that can be resized
// says how far.
func TestMachineLineShowsTheMaximums(t *testing.T) {
	tests := []struct {
		name string
		spec dicer.InstanceSpec
		want string
	}{
		{
			name: "none",
			spec: dicer.InstanceSpec{VCPUs: 2, MemoryBytes: 1 << 30, DiskBytes: 10 << 30},
			want: "2 vCPUs, 1 GiB memory, 10 GiB disk",
		},
		{
			name: "both",
			spec: dicer.InstanceSpec{
				VCPUs: 2, MemoryBytes: 1 << 30, DiskBytes: 10 << 30, MaxVCPUs: 8, MaxMemoryBytes: 4 << 30,
			},
			want: "2 vCPUs (up to 8), 1 GiB memory (up to 4 GiB), 10 GiB disk",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := machineLines(dicer.Instance{InstanceSpec: tt.spec})[0]; got != tt.want {
				t.Errorf("machine line = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLimitsDetailNamesOnlyTheLimitsSet checks that inspect's Limits line
// lists the limits an instance has, and is empty for one with none.
func TestLimitsDetailNamesOnlyTheLimitsSet(t *testing.T) {
	tests := []struct {
		name string
		spec dicer.InstanceSpec
		want string
	}{
		{"none", dicer.InstanceSpec{}, ""},
		{"disk IOPS", dicer.InstanceSpec{DiskIOPS: 1000}, "disk 1000 IOPS each"},
		{
			"all",
			dicer.InstanceSpec{
				DiskBytesPerSecond: 50 << 20, DiskIOPS: 1000, UploadBytesPerSecond: 1 << 20, DownloadBytesPerSecond: 2 << 20,
			},
			"disk 50 MiB/s and 1000 IOPS each, upload 1 MiB/s, download 2 MiB/s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := limitsDetail(dicer.Instance{InstanceSpec: tt.spec}); got != tt.want {
				t.Errorf("limits = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMountLinesDescribeAFileBySizeAndMode(t *testing.T) {
	got := mountLines([]dicer.Mount{
		{Type: dicer.MountTypeFile, Target: "/etc/app.conf", Content: []byte("k=v\n"), Mode: 0o640, ReadOnly: true},
		{Type: dicer.MountTypeFile, Target: "/etc/empty"},
		{Type: dicer.MountTypeVolume, Source: "data", Target: "/data"},
	})
	want := []string{
		"file on /etc/app.conf (4 B, mode 0640, read-only)",
		"file on /etc/empty (0 B, mode 0644)",
		"volume data on /data",
	}
	if !slices.Equal(got, want) {
		t.Errorf("mountLines = %q, want %q", got, want)
	}
}
