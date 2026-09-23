// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package process

import (
	"errors"
	"os/exec"
	"testing"
	"time"
)

// waitDone fails the test if p has not exited within a generous bound.
func waitDone(t *testing.T, p *Process) {
	t.Helper()

	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("process %d did not exit", p.PID())
	}
}

func TestStartReportsKill(t *testing.T) {
	p, err := Start(exec.CommandContext(t.Context(), "sleep", "60"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case <-p.Done():
		t.Fatal("Done closed before the process exited")
	default:
	}

	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitDone(t, p)

	var exitErr *exec.ExitError
	if !errors.As(p.Err(), &exitErr) {
		t.Fatalf("Err = %v, want an *exec.ExitError", p.Err())
	}
	if got := exitErr.String(); got != "signal: killed" {
		t.Errorf("Err = %q, want signal: killed", got)
	}
}

func TestStartReportsCleanExit(t *testing.T) {
	p, err := Start(exec.CommandContext(t.Context(), "true"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, p)

	if p.Err() != nil {
		t.Errorf("Err = %v, want nil for a clean exit", p.Err())
	}
}

func TestKillAfterExit(t *testing.T) {
	p, err := Start(exec.CommandContext(t.Context(), "true"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, p)

	if err := p.Kill(); err != nil {
		t.Errorf("Kill after exit = %v, want nil", err)
	}
	p.Terminate()
}

func TestStartFailure(t *testing.T) {
	if _, err := Start(exec.CommandContext(t.Context(), "/nonexistent/binary")); err == nil {
		t.Error("Start of a missing binary succeeded")
	}
}
