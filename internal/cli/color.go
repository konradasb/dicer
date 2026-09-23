// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"io"
	"os"
)

// palette colours what a person reads at a terminal, and nothing else: output
// piped to a file or a program is left plain, as it is for anyone who sets
// NO_COLOR (https://no-color.org).
//
// Colour is kept for what it says at a glance -- whether something is
// working -- and never carries meaning the text does not.
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

// status colours a word that says how something is doing: a lifecycle state
// or a health verdict. Working is green, in between is yellow, and broken is
// red; stopped is neither, and stays plain.
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

// event colours s, an event's action, by what it says: green for something
// coming up or recovering, yellow for something going down or put off, red
// for something failing. What happened to a definition is plain.
func (p palette) event(action, s string) string {
	switch action {
	case "started", "resumed", "healthy", "pulled":
		return p.paint(ansiGreen, s)
	case "stopped", "paused", "restarting", "collected":
		return p.paint(ansiYellow, s)
	case "died", "unhealthy":
		return p.paint(ansiRed, s)
	default:
		return s
	}
}
