// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/dicer-sh/dicer/internal/guest"
)

// The agent's request to shut down reaches the workload as the SIGTERM it
// stops on; other signals reach it as they are.
func TestForwardSignals(t *testing.T) {
	for sent, want := range map[os.Signal]int{
		guest.ShutdownSignal: 128 + int(syscall.SIGTERM),
		syscall.SIGINT:       128 + int(syscall.SIGINT),
	} {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		signals := make(chan os.Signal, 1)
		go forwardSignals(slog.New(slog.DiscardHandler), signals, cmd.Process)
		signals <- sent

		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("%v was not passed on", sent)
		}
		close(signals)

		if got := guest.ExitStatus(cmd.ProcessState); got != want {
			t.Errorf("sent %v: the workload ended with %d, want %d", sent, got, want)
		}
	}
}
