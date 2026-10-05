// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import "testing"

func TestRuleComment(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"unquoted", `-A DICER-FORWARD -i dicer-ab -o dicer-ab -m comment --comment dicer-icc-dicer-ab -j ACCEPT`, "dicer-icc-dicer-ab"},
		{"quoted", `-A DICER-FORWARD -m comment --comment "dicer-icc-dicer-a" -j ACCEPT`, "dicer-icc-dicer-a"},
		{"no comment", `-A DICER-FORWARD -i eth0 -j ACCEPT`, ""},
		{"chain", `-N DICER-FORWARD`, ""},
		{"empty line", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ruleComment(tt.line); got != tt.want {
				t.Errorf("ruleComment(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

// A bridge whose name is a prefix of another's must not be taken for it.
func TestCommentsOfPrefixedBridgesDiffer(t *testing.T) {
	line := `-A DICER-FORWARD -i dicer-ab -o dicer-ab -m comment --comment ` + iccComment("dicer-ab") + ` -j ACCEPT`
	if ruleComment(line) == iccComment("dicer-a") {
		t.Error("dicer-a's rule was found in dicer-ab's")
	}
}
