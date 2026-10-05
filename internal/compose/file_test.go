// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"slices"
	"testing"
)

func TestSplitShellWords(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"nginx -g 'daemon off;'", []string{"nginx", "-g", "daemon off;"}},
		{`sh -c "echo \"hi\" \$HOME"`, []string{"sh", "-c", `echo "hi" $HOME`}},
		{`a\ b  c`, []string{"a b", "c"}},
		{`''`, []string{""}},
		{"  spaced\tout  ", []string{"spaced", "out"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := splitShellWords(tt.in)
			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("splitShellWords(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}

	for _, in := range []string{`echo "open`, `echo 'open`, `trailing\`} {
		if _, err := splitShellWords(in); err == nil {
			t.Errorf("splitShellWords(%q) succeeded, want an error", in)
		}
	}
}
