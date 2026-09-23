// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hostinfo

import (
	"strings"
	"testing"
)

func TestParseMemTotal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{
			name:  "typical",
			input: "MemTotal:       16384000 kB\nMemFree:         2048000 kB\nHugePages_Total:       4\n",
			want:  16384000 * 1024,
		},
		{
			name:  "not first, among values with colons",
			input: "SomeKey:  some:value\nMemTotal:        4096 kB\n",
			want:  4096 * 1024,
		},
		{name: "missing", input: "MemFree:          1024 kB\n", wantErr: true},
		{name: "empty", input: "", wantErr: true},
		{name: "malformed", input: "MemTotal:  lots kB\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMemTotal(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseMemTotal() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseMemTotal() = %d, want %d", got, tt.want)
			}
		})
	}
}
