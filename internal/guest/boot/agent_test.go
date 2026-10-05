// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"os"
	"path/filepath"
	"testing"
)

// An installed agent the same size as the initrd's is still replaced if it
// differs: two builds are easily the same size, and an instance must get the
// agent of the daemon that booted it.
func TestHasContentsComparesBytesNotSizes(t *testing.T) {
	want := []byte("agent v2")
	installed := filepath.Join(t.TempDir(), "dicer-agent")

	tests := []struct {
		name     string
		contents []byte // nil for no installed file
		want     bool
	}{
		{name: "same size, different bytes", contents: []byte("agent v1"), want: false},
		{name: "identical", contents: want, want: true},
		{name: "nothing installed", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(installed)
			if tt.contents != nil {
				if err := os.WriteFile(installed, tt.contents, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			got, err := hasContents(installed, want)
			if err != nil {
				t.Fatalf("hasContents: %v", err)
			}
			if got != tt.want {
				t.Errorf("hasContents = %v, want %v", got, tt.want)
			}
		})
	}
}
