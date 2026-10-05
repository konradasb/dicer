// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
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
		t.Run(tt.in, func(t *testing.T) {
			got, err := interpolate(tt.in, lookup)
			if err != nil || got != tt.want {
				t.Errorf("interpolate(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
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
		t.Run(tt.in, func(t *testing.T) {
			_, err := interpolate(tt.in, lookup)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("interpolate(%q) = %v, want an error saying %q", tt.in, err, tt.want)
			}
		})
	}

	// Set but empty counts only with the colon.
	if got, err := interpolate("${EMPTY?unused}", lookup); err != nil || got != "" {
		t.Errorf("interpolate(${EMPTY?unused}) = %q, %v; want \"\", nil", got, err)
	}
}
