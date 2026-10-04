// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/humanize"
)

// spinnerFrames are drawn in turn while a task runs.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerInterval is how often the spinner moves.
const spinnerInterval = 80 * time.Millisecond

// task is a slow operation shown as a spinner on a terminal, then a line
// saying how it went.
type task struct {
	out   io.Writer
	start time.Time

	stop chan struct{}
	done sync.WaitGroup
}

// startTask begins a task described by what, e.g. "Starting web".
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
			_, _ = fmt.Fprintf(t.out, "\r\x1b[K%s %s… %s", frame, what, humanize.Duration(time.Since(t.start)))

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

// succeeded writes a line saying something went well.
func succeeded(cmd *cobra.Command, format string, args ...any) {
	cmd.PrintErrf(format+"\n", args...)
}
