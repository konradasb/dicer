// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import "testing"

func TestStatusRoundTrip(t *testing.T) {
	code := 137
	tests := []struct {
		name   string
		status Status
	}{
		{name: "never booted", status: Status{}},
		{name: "booted", status: Status{Boots: 1}},
		{name: "exited", status: Status{Boots: 1, ExitCode: &code}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.status.Encode()
			if len(data) != StatusSize {
				t.Fatalf("Encode is %d bytes, want %d", len(data), StatusSize)
			}

			got, err := DecodeStatus(data)
			if err != nil {
				t.Fatalf("DecodeStatus(Encode()): %v", err)
			}
			if got.Boots != tt.status.Boots || (got.ExitCode == nil) != (tt.status.ExitCode == nil) ||
				(got.ExitCode != nil && *got.ExitCode != *tt.status.ExitCode) {
				t.Errorf("DecodeStatus(Encode(%+v)) = %+v", tt.status, got)
			}
		})
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
