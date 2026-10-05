// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	in := `
# a comment
TAG=1.27
export PORT = 8080
QUOTED="a b"
SINGLE='$not expanded'
EQUALS=a=b
BARE
`
	got, err := parseEnvFile(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"TAG": "1.27", "PORT": "8080", "QUOTED": "a b", "SINGLE": "$not expanded", "EQUALS": "a=b",
	}
	for k, v := range want {
		if got[k] == nil || *got[k] != v {
			t.Errorf("%s = %v, want %q", k, got[k], v)
		}
	}
	if v, ok := got["BARE"]; !ok || v != nil {
		t.Errorf("BARE = %v, %v; want recorded with no value", v, ok)
	}

	if _, err := parseEnvFile(strings.NewReader("A B=1\n")); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("a key with a space = %v, want an error naming the line", err)
	}
}
