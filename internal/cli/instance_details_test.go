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
