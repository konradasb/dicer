// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// minWatchInterval keeps --watch from hammering the daemon.
const minWatchInterval = 200 * time.Millisecond

// Terminal control sequences a screen draws with.
const (
	clearScreen = "\x1b[H\x1b[2J"
	hideCursor  = "\x1b[?25l"
	showCursor  = "\x1b[?25h"
)

// screen shows successive frames of a live view: on a terminal, each in
// place of the last under a header, and elsewhere one after another.
type screen struct {
	out      io.Writer
	terminal bool
	drawn    bool
}

// newScreen returns a screen writing to out. On a terminal it hides the
// cursor until close.
func newScreen(out io.Writer) *screen {
	s := &screen{out: out, terminal: isTerminal(out)}
	if s.terminal {
		_, _ = io.WriteString(out, hideCursor)
	}
	return s
}

// draw shows frame, under header on a terminal.
func (s *screen) draw(header string, frame []byte) {
	switch {
	case s.terminal:
		_, _ = fmt.Fprintf(s.out, "%s%s\n\n%s", clearScreen, header, frame)
	case s.drawn:
		_, _ = fmt.Fprintf(s.out, "\n%s", frame)
	default:
		_, _ = s.out.Write(frame)
	}
	s.drawn = true
}

// close gives the cursor back.
func (s *screen) close() {
	if s.terminal {
		_, _ = io.WriteString(s.out, showCursor)
	}
}

// watchList redraws what list writes every interval until Ctrl+C. Errors are
// shown in place of the list and retried.
func watchList(
	cmd *cobra.Command, interval time.Duration, list func(ctx context.Context, w io.Writer) error,
) error {
	if interval < minWatchInterval {
		return usagef(cmd, "invalid --interval %s: it must be at least %s", interval, minWatchInterval)
	}

	ctx, stop := signal.NotifyContext(contextOf(cmd), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	screen := newScreen(cmd.OutOrStdout())
	defer screen.close()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// Drawn off screen and written at once, so the screen never shows
		// half a list.
		var frame bytes.Buffer
		if err := list(ctx, &frame); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			frame.Reset()
			fmt.Fprintf(&frame, "Error: %s\n", err)
		}

		screen.draw(fmt.Sprintf("Every %s · %s · Ctrl+C to stop", interval, time.Now().Format(time.TimeOnly)),
			frame.Bytes())

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
