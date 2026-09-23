// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// run runs a shell script as Exec does, under ctx, and returns the exit
// status it would report.
func run(ctx context.Context, script string) int32 {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	return exitCodeOf(ctx, cmd, cmd.Run())
}

func TestExitCodeOf(t *testing.T) {
	tests := []struct {
		script string
		want   int32
	}{
		{"exit 0", 0},
		{"exit 3", 3},
		// Killed, but not by the timeout: the OOM killer, say. A shell
		// reports 128 plus the signal, not a timeout.
		{"kill -9 $$", 137},
		{"kill -15 $$", 143},
	}
	for _, tt := range tests {
		if got := run(t.Context(), tt.script); got != tt.want {
			t.Errorf("%q = %d, want %d", tt.script, got, tt.want)
		}
	}
}

func TestExitCodeOfTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	if got := run(ctx, "sleep 10"); got != exitTimedOut {
		t.Errorf("a command killed by its timeout = %d, want %d", got, exitTimedOut)
	}
}

// A command that exits on its own is reported as it exited, even if the
// deadline has passed by the time it is looked at.
func TestExitCodeOfExitAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
	cmd := exec.CommandContext(ctx, "sh", "-c", "exit 5")
	err := cmd.Run()
	cancel()

	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	if got := exitCodeOf(expired, cmd, err); got != 5 {
		t.Errorf("exit 5 looked at after the deadline = %d, want 5", got)
	}
}
