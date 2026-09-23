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

// Terminal control sequences --watch draws with.
const (
	clearScreen = "\x1b[H\x1b[2J"
	hideCursor  = "\x1b[?25l"
	showCursor  = "\x1b[?25h"
)

// watchList draws what list writes, again every interval, until Ctrl+C. On
// a terminal each drawing replaces the last, under a line saying when it
// was drawn; anywhere else they follow each other, a blank line between.
//
// A call that fails is shown in place of the list and tried again, so a
// daemon that restarts under a watch is waited for rather than fatal.
func watchList(
	cmd *cobra.Command, interval time.Duration, list func(ctx context.Context, w io.Writer) error,
) error {
	if interval < minWatchInterval {
		return usagef(cmd, "invalid --interval %s: it must be at least %s", interval, minWatchInterval)
	}

	ctx, stop := signal.NotifyContext(contextOf(cmd), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	out := cmd.OutOrStdout()
	terminal := isTerminal(out)

	if terminal {
		_, _ = io.WriteString(out, hideCursor)
		defer func() { _, _ = io.WriteString(out, showCursor) }()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for first := true; ; first = false {
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

		switch {
		case terminal:
			header := fmt.Sprintf("Every %s · %s · Ctrl+C to stop", interval, time.Now().Format(time.TimeOnly))
			_, _ = fmt.Fprintf(out, "%s%s\n\n%s", clearScreen, header, frame.Bytes())
		case !first:
			_, _ = fmt.Fprintf(out, "\n%s", frame.Bytes())
		default:
			_, _ = out.Write(frame.Bytes())
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
