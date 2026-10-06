// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"strings"
	"testing"
)

// The guest kills a command after whole seconds, so --timeout is rounded up
// to them: a timeout under a second must not become no limit at all.
func TestExecTimeoutIsRoundedUpToWholeSeconds(t *testing.T) {
	tests := []struct {
		timeout string
		want    int32
	}{
		{"0", 0},
		{"30s", 30},
		{"2m", 120},
		{"1500ms", 2},
		{"1ms", 1},
	}
	for _, tt := range tests {
		t.Run(tt.timeout, func(t *testing.T) {
			cmd := newInstanceExecCommand()
			if err := cmd.ParseFlags([]string{"--timeout", tt.timeout}); err != nil {
				t.Fatal(err)
			}

			start, err := buildExecStart(cmd, []string{"web", "true"})
			if err != nil {
				t.Fatalf("buildExecStart: %v", err)
			}
			if got := start.GetTimeoutSeconds(); got != tt.want {
				t.Errorf("--timeout %s = %d seconds, want %d", tt.timeout, got, tt.want)
			}
		})
	}
}

// A bare number says nothing of its unit, so it is refused rather than taken
// as seconds.
func TestExecTimeoutNeedsAUnit(t *testing.T) {
	isolateConfig(t)

	_, err := run(t, "exec", "--timeout", "30", "web", "true")
	if err == nil || !strings.Contains(err.Error(), "want a duration like 30s") {
		t.Errorf("exec --timeout 30 = %v, want it to ask for a duration", err)
	}
}
