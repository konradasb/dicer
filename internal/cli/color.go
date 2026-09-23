// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"io"
	"os"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// palette colours terminal output. Output to a pipe, or with NO_COLOR set
// (https://no-color.org), is left plain.
type palette struct {
	enabled bool
}

// paletteFor returns the palette for output written to w.
func paletteFor(w io.Writer) palette {
	return palette{enabled: isTerminal(w) && os.Getenv("NO_COLOR") == ""}
}

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

func (p palette) paint(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return code + s + ansiReset
}

func (p palette) bold(s string) string { return p.paint(ansiBold, s) }

// accent marks Dicer's own: the logo.
func (p palette) accent(s string) string { return p.paint(ansiCyan, s) }

// level colours s by how full something is, a fraction: green with room to
// spare, yellow from 70%, red from 90%.
func (p palette) level(fraction float64, s string) string {
	switch {
	case fraction >= 0.9:
		return p.paint(ansiRed, s)
	case fraction >= 0.7:
		return p.paint(ansiYellow, s)
	default:
		return p.paint(ansiGreen, s)
	}
}

// status colours a lifecycle state or health status: green for working,
// yellow for in between, red for broken, plain for stopped.
func (p palette) status(word string) string {
	switch word {
	case "running", "healthy":
		return p.paint(ansiGreen, word)
	case "starting", "stopping", "restarting", "paused":
		return p.paint(ansiYellow, word)
	case "failed", "unhealthy":
		return p.paint(ansiRed, word)
	default:
		return word
	}
}

// dot is the mark before a name that says, in colour, how it is doing; in
// plain output it is a dot all the same.
func (p palette) dot(state string) string {
	switch state {
	case "running":
		return p.paint(ansiGreen, "●")
	case "starting", "stopping", "restarting", "paused":
		return p.paint(ansiYellow, "●")
	case "failed":
		return p.paint(ansiRed, "●")
	default:
		return "○"
	}
}

// event colours an event's action: green for coming up, yellow for going
// down, red for failing, plain for definition changes.
func (p palette) event(action dicerdv1.EventAction, s string) string {
	switch action {
	case dicerdv1.EventAction_EVENT_ACTION_STARTED, dicerdv1.EventAction_EVENT_ACTION_RESUMED,
		dicerdv1.EventAction_EVENT_ACTION_HEALTHY, dicerdv1.EventAction_EVENT_ACTION_PULLED:
		return p.paint(ansiGreen, s)
	case dicerdv1.EventAction_EVENT_ACTION_STOPPED, dicerdv1.EventAction_EVENT_ACTION_PAUSED,
		dicerdv1.EventAction_EVENT_ACTION_RESTARTING, dicerdv1.EventAction_EVENT_ACTION_COLLECTED:
		return p.paint(ansiYellow, s)
	case dicerdv1.EventAction_EVENT_ACTION_DIED, dicerdv1.EventAction_EVENT_ACTION_UNHEALTHY:
		return p.paint(ansiRed, s)
	default:
		return s
	}
}
