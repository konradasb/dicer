// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import (
	"os/exec"
	"testing"
)

func TestExitStatus(t *testing.T) {
	tests := map[string]int{
		"exit 0":      0,
		"exit 3":      3,
		"kill -9 $$":  137,
		"kill -15 $$": 143,
	}
	for script, want := range tests {
		t.Run(script, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "sh", "-c", script)
			_ = cmd.Run()
			if got := ExitStatus(cmd.ProcessState); got != want {
				t.Errorf("ExitStatus = %d, want %d", got, want)
			}
		})
	}
}
