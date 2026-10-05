// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import "testing"

func TestCPUAdded(t *testing.T) {
	tests := []struct {
		event string
		want  bool
	}{
		{"add@/devices/system/cpu/cpu3\x00ACTION=add\x00DEVPATH=/devices/system/cpu/cpu3\x00SUBSYSTEM=cpu\x00", true},
		{"add@/devices/system/cpu/cpu12\x00", true},
		{"remove@/devices/system/cpu/cpu3\x00", false},
		{"online@/devices/system/cpu/cpu3\x00", false},
		{"add@/devices/system/cpu/cpufreq\x00", false},
		{"add@/devices/system/memory/memory40\x00", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := cpuAdded([]byte(tt.event)); got != tt.want {
			t.Errorf("cpuAdded(%q) = %t, want %t", tt.event, got, tt.want)
		}
	}
}
