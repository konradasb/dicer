// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package bytesize

import "testing"

func TestFormat(t *testing.T) {
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
		if got := Format(tc.bytes); got != tc.want {
			t.Errorf("Format(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

// TestFormatOfWritesBothInTheTotalsUnit checks an amount is written in its
// total's unit, so the two read as one.
func TestFormatOfWritesBothInTheTotalsUnit(t *testing.T) {
	for _, tc := range []struct {
		part, total int64
		want        string
	}{
		{512 << 20, 2 << 30, "0.5 of 2 GiB"},
		{1 << 30, 1 << 30, "1 of 1 GiB"},
		{0, 0, "0 of 0 B"},
		{3 << 20, 900 << 20, "3 of 900 MiB"},
	} {
		if got := FormatOf(tc.part, tc.total); got != tc.want {
			t.Errorf("FormatOf(%d, %d) = %q, want %q", tc.part, tc.total, got, tc.want)
		}
	}
}
