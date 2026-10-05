// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"testing"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// TestMachineLineShowsTheMaximums checks that an instance that can be resized
// says how far.
func TestMachineLineShowsTheMaximums(t *testing.T) {
	tests := []struct {
		name     string
		instance *dicerdv1.Instance
		want     string
	}{
		{
			name:     "none",
			instance: &dicerdv1.Instance{Vcpus: 2, MemoryBytes: 1 << 30, DiskBytes: 10 << 30},
			want:     "2 vCPUs, 1 GiB memory, 10 GiB disk",
		},
		{
			name: "both",
			instance: &dicerdv1.Instance{
				Vcpus: 2, MemoryBytes: 1 << 30, DiskBytes: 10 << 30, MaxVcpus: 8, MaxMemoryBytes: 4 << 30,
			},
			want: "2 vCPUs (up to 8), 1 GiB memory (up to 4 GiB), 10 GiB disk",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := machineLines(tt.instance)[0]; got != tt.want {
				t.Errorf("machine line = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLimitsDetailNamesOnlyTheLimitsSet checks that inspect's Limits line
// lists the limits an instance has, and is empty for one with none.
func TestLimitsDetailNamesOnlyTheLimitsSet(t *testing.T) {
	tests := []struct {
		name     string
		instance *dicerdv1.Instance
		want     string
	}{
		{"none", &dicerdv1.Instance{}, ""},
		{"disk IOPS", &dicerdv1.Instance{DiskIops: 1000}, "disk 1000 IOPS each"},
		{
			"all",
			&dicerdv1.Instance{
				DiskBytesPerSecond: 50 << 20, DiskIops: 1000, UploadBytesPerSecond: 1 << 20, DownloadBytesPerSecond: 2 << 20,
			},
			"disk 50 MiB/s and 1000 IOPS each, upload 1 MiB/s, download 2 MiB/s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := limitsDetail(tt.instance); got != tt.want {
				t.Errorf("limits = %q, want %q", got, tt.want)
			}
		})
	}
}
