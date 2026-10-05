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

	"github.com/konradasb/dicer/internal/guest"
)

// The guest agent's request to shut down reaches the workload as the SIGTERM
// it stops on; other signals reach it as they are.
func TestForwardSignals(t *testing.T) {
	tests := []struct {
		name string
		sent os.Signal
		want int
	}{
		{name: "shutdown becomes SIGTERM", sent: guest.ShutdownSignal, want: 128 + int(syscall.SIGTERM)},
		{name: "SIGINT is passed on", sent: syscall.SIGINT, want: 128 + int(syscall.SIGINT)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("sleep", "30")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}

			signals := make(chan os.Signal, 1)
			defer close(signals)
			go forwardSignals(slog.New(slog.DiscardHandler), signals, cmd.Process)
			signals <- tt.sent

			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("%v was not passed on", tt.sent)
			}

			if got := guest.ExitStatus(cmd.ProcessState); got != tt.want {
				t.Errorf("the workload ended with %d, want %d", got, tt.want)
			}
		})
	}
}
