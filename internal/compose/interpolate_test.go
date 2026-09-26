// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"slices"
	"strings"
	"testing"
)

func lookupIn(vars map[string]string) Lookup {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

func TestInterpolate(t *testing.T) {
	lookup := lookupIn(map[string]string{"TAG": "1.27", "EMPTY": "", "PORT": "8080"})

	tests := []struct {
		in, want string
	}{
		{"nginx:$TAG", "nginx:1.27"},
		{"nginx:${TAG}", "nginx:1.27"},
		{"${UNSET}", ""},
		{"${UNSET:-1.26}", "1.26"},
		{"${EMPTY:-fallback}", "fallback"},
		{"${EMPTY-fallback}", ""},
		{"${UNSET-fallback}", "fallback"},
		{"${TAG:+set}", "set"},
		{"${EMPTY:+set}", ""},
		{"${EMPTY+set}", "set"},
		{"${UNSET+set}", ""},
		{"${UNSET:-${PORT}}:80", "8080:80"},
		{"${UNSET:-${ALSO_UNSET:-9090}}", "9090"},
		{"cost $$5", "cost $5"},
		{"$1 and a lone $", "$1 and a lone $"},
		{"${TAG}${PORT}", "1.278080"},
	}
	for _, tt := range tests {
		got, err := interpolate(tt.in, lookup)
		if err != nil {
			t.Errorf("interpolate(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("interpolate(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestInterpolateFails(t *testing.T) {
	lookup := lookupIn(map[string]string{"EMPTY": ""})

	tests := []struct {
		in, want string
	}{
		{"${UNSET:?set a password}", "required variable UNSET: set a password"},
		{"${EMPTY:?}", "required variable EMPTY: it is not set"},
		{"${UNCLOSED", "no closing }"},
		{"${}", "want a variable name"},
		{"${1ABC}", "want a variable name"},
		{"${A*b}", "want :-, -, :?, ?, :+ or +"},
	}
	for _, tt := range tests {
		_, err := interpolate(tt.in, lookup)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("interpolate(%q) = %v, want an error saying %q", tt.in, err, tt.want)
		}
	}

	// Set but empty counts only with the colon.
	if got, err := interpolate("${EMPTY?unused}", lookup); err != nil || got != "" {
		t.Errorf("interpolate(${EMPTY?unused}) = %q, %v; want \"\", nil", got, err)
	}
}

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
		got, err := splitShellWords(tt.in)
		if err != nil {
			t.Errorf("splitShellWords(%q): %v", tt.in, err)
			continue
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("splitShellWords(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	for _, in := range []string{`echo "open`, `echo 'open`, `trailing\`} {
		if _, err := splitShellWords(in); err == nil {
			t.Errorf("splitShellWords(%q) succeeded, want an error", in)
		}
	}
}
