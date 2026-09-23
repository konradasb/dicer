// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// spinnerFrames are drawn in turn while a task runs.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerInterval is how often the spinner moves.
const spinnerInterval = 80 * time.Millisecond

// task is something slow a command is waiting on, shown on a terminal as a
// spinner and how long it has taken so far, and afterwards as one line
// saying how it went and how long it took.
type task struct {
	out   io.Writer
	start time.Time

	stop chan struct{}
	done sync.WaitGroup
}

// startTask begins a task described by what, e.g. "Starting web". The
// spinner is drawn only on a terminal; anywhere else nothing is written
// until the task ends.
func startTask(cmd *cobra.Command, what string) *task {
	t := &task{out: cmd.ErrOrStderr(), start: time.Now()}
	if !isTerminal(t.out) {
		return t
	}

	t.stop = make(chan struct{})
	t.done.Go(func() {
		ticker := time.NewTicker(spinnerInterval)
		defer ticker.Stop()

		for i := 0; ; i++ {
			frame := spinnerFrames[i%len(spinnerFrames)]
			_, _ = fmt.Fprintf(t.out, "\r\x1b[K%s %s… %s", frame, what, formatDuration(time.Since(t.start)))

			select {
			case <-t.stop:
				_, _ = io.WriteString(t.out, "\r\x1b[K")
				return
			case <-ticker.C:
			}
		}
	})

	return t
}

// elapsed is how long the task has taken.
func (t *task) elapsed() time.Duration { return time.Since(t.start) }

// end stops the spinner, leaving the line clear.
func (t *task) end() {
	if t.stop == nil {
		return
	}
	close(t.stop)
	t.done.Wait()
	t.stop = nil
}

// succeed ends the task with a line saying what came of it.
func (t *task) succeed(format string, args ...any) {
	t.end()
	_, _ = fmt.Fprintf(t.out, format+"\n", args...)
}

// formatDuration renders how long something took, to a precision a person
// cares about: "120ms", "1.4s", "2m5s".
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// succeeded writes a line saying something went well.
func succeeded(cmd *cobra.Command, format string, args ...any) {
	cmd.PrintErrf(format+"\n", args...)
}
