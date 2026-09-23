// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hostinfo

import (
	"strings"
	"testing"
)

func TestParseCPUCount(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{
			name: "several processors",
			input: "processor\t: 0\nvendor_id\t: GenuineIntel\nphysical id\t: 0\n\n" +
				"processor\t: 1\nphysical id\t: 0\n\n" +
				"processor\t: 2\nphysical id\t: 1\n",
			want: 3,
		},
		{
			name:  "arm64, no trailing blank line",
			input: "processor\t: 0\nBogoMIPS\t: 48.00\nFeatures\t: fp asimd\n",
			want:  1,
		},
		{name: "empty", input: "", wantErr: true},
		{name: "no processor entries", input: "vendor_id\t: GenuineIntel\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCPUCount(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCPUCount() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseCPUCount() = %d, want %d", got, tt.want)
			}
		})
	}
}
