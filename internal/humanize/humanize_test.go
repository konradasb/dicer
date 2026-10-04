// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package humanize

import (
	"testing"
	"time"
)

func TestBytes(t *testing.T) {
	for _, tc := range []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{512 << 20, "512 MiB"},
		{1 << 30, "1 GiB"},
		{1536 << 20, "1.5 GiB"},
		{33_554_432_000, "31.3 GiB"},
	} {
		if got := Bytes(tc.bytes); got != tc.want {
			t.Errorf("Bytes(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

// TestBytesOfWritesBothInTheTotalsUnit checks an amount is written in its
// total's unit, so the two read as one.
func TestBytesOfWritesBothInTheTotalsUnit(t *testing.T) {
	for _, tc := range []struct {
		part, total int64
		want        string
	}{
		{512 << 20, 2 << 30, "0.5 of 2 GiB"},
		{1 << 30, 1 << 30, "1 of 1 GiB"},
		{0, 0, "0 of 0 B"},
		{3 << 20, 900 << 20, "3 of 900 MiB"},
	} {
		if got := BytesOf(tc.part, tc.total); got != tc.want {
			t.Errorf("BytesOf(%d, %d) = %q, want %q", tc.part, tc.total, got, tc.want)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		829 * time.Millisecond:                "829ms",
		1840 * time.Millisecond:               "1.8s",
		5*time.Minute + 3400*time.Millisecond: "5m3s",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCount(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0 vCPUs"},
		{1, "1 vCPU"},
		{4, "4 vCPUs"},
	} {
		if got := Count(tc.n, "vCPU"); got != tc.want {
			t.Errorf("Count(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
