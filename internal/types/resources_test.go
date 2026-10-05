// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import "testing"

func TestCapacityAllocatable(t *testing.T) {
	// A host with 4 CPUs and 8GiB, overcommitted as the daemon does by
	// default: 16 vCPUs, and 7GiB once 1GiB is reserved.
	capacity := Capacity{
		Host:                Resources{VCPUs: 4, MemoryBytes: 8 << 30},
		ReservedMemoryBytes: 1 << 30,
		CPUOvercommit:       4,
		MemoryOvercommit:    1,
	}
	want := Resources{VCPUs: 16, MemoryBytes: 7 << 30}
	if got := capacity.Allocatable(); got != want {
		t.Errorf("Allocatable = %+v, want %+v", got, want)
	}
}
