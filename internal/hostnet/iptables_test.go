// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import "testing"

func TestSpecComment(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{`-A DICER-FORWARD -i dicer-ab -o dicer-ab -m comment --comment dicer-icc-dicer-ab -j ACCEPT`, "dicer-icc-dicer-ab"},
		{`-A DICER-FORWARD -m comment --comment "dicer-icc-dicer-a" -j ACCEPT`, "dicer-icc-dicer-a"},
		{`-A DICER-FORWARD -i eth0 -j ACCEPT`, ""},
		{`-N DICER-FORWARD`, ""},
		{``, ""},
	}
	for _, tt := range tests {
		if got := specComment(tt.line); got != tt.want {
			t.Errorf("specComment(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

// A bridge whose name is a prefix of another's must not be taken for it.
func TestCommentsOfPrefixedBridgesDiffer(t *testing.T) {
	line := `-A DICER-FORWARD -i dicer-ab -o dicer-ab -m comment --comment ` + iccComment("dicer-ab") + ` -j ACCEPT`
	if specComment(line) == iccComment("dicer-a") {
		t.Error("dicer-a's rule was found in dicer-ab's")
	}
}
