// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package humanize

import (
	"testing"
	"time"
)

// TestBytesUsesLargestWholeUnit checks a size is written in the largest unit
// that leaves it at least 1, to one decimal place.
func TestBytesUsesLargestWholeUnit(t *testing.T) {
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
		t.Run(tc.want, func(t *testing.T) {
			if got := Bytes(tc.bytes); got != tc.want {
				t.Errorf("Bytes(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		})
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
		t.Run(tc.want, func(t *testing.T) {
			if got := BytesOf(tc.part, tc.total); got != tc.want {
				t.Errorf("BytesOf(%d, %d) = %q, want %q", tc.part, tc.total, got, tc.want)
			}
		})
	}
}

func TestNumberDropsATrailingZero(t *testing.T) {
	for _, tc := range []struct {
		f    float64
		want string
	}{
		{4, "4"},
		{1.04, "1"},
		{30.25, "30.3"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := Number(tc.f); got != tc.want {
				t.Errorf("Number(%v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}

// TestDurationRoundsToAUsefulPrecision checks a duration is rounded more
// coarsely the longer it is.
func TestDurationRoundsToAUsefulPrecision(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{829 * time.Millisecond, "829ms"},
		{1840 * time.Millisecond, "1.8s"},
		{5*time.Minute + 3400*time.Millisecond, "5m3s"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := Duration(tc.d); got != tc.want {
				t.Errorf("Duration(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestCountPluralisesAnyNumberButOne(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0 vCPUs"},
		{1, "1 vCPU"},
		{4, "4 vCPUs"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := Count(tc.n, "vCPU"); got != tc.want {
				t.Errorf("Count(%d) = %q, want %q", tc.n, got, tc.want)
			}
		})
	}
}
