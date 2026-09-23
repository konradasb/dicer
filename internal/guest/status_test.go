// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import (
	"testing"
)

func TestStatusRoundTrip(t *testing.T) {
	code := 137
	for _, st := range []Status{
		{},
		{Boots: 1},
		{Boots: 1, ExitCode: &code},
	} {
		data := st.Encode()
		if len(data) != StatusSize {
			t.Fatalf("Encode(%+v) is %d bytes, want %d", st, len(data), StatusSize)
		}

		got, err := DecodeStatus(data)
		if err != nil {
			t.Fatalf("DecodeStatus(Encode(%+v)): %v", st, err)
		}
		if got.Boots != st.Boots || (got.ExitCode == nil) != (st.ExitCode == nil) ||
			(got.ExitCode != nil && *got.ExitCode != *st.ExitCode) {
			t.Errorf("DecodeStatus(Encode(%+v)) = %+v", st, got)
		}
	}
}

// A disk the host zeroed and nothing booted from reads as the zero Status.
func TestDecodeStatusZeroedDisk(t *testing.T) {
	st, err := DecodeStatus(make([]byte, StatusSize))
	if err != nil {
		t.Fatalf("DecodeStatus of zeroes: %v", err)
	}
	if st.Boots != 0 || st.ExitCode != nil {
		t.Errorf("DecodeStatus of zeroes = %+v, want the zero Status", st)
	}
}

func TestDecodeStatusRejectsGarbage(t *testing.T) {
	if _, err := DecodeStatus([]byte("not json")); err == nil {
		t.Error("DecodeStatus accepted garbage")
	}
}
