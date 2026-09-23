// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/dicer-sh/dicer"
)

// progressInterval is how often a terminal's progress line is redrawn. Any
// faster is wasted on a reader.
const progressInterval = 100 * time.Millisecond

// pullReporter renders a pull's progress.
//
// On a terminal it rewrites one line as the pull runs; anywhere else -- a
// pipe, a log, CI -- it prints a line per stage, since carriage returns in a
// log file help nobody.
type pullReporter struct {
	out      io.Writer
	terminal bool

	stage      dicer.PullStage
	stageStart time.Time
	lastDraw   time.Time

	// fetched is whether the pull did anything beyond checking the
	// registry: an image already on the host goes no further than
	// resolving, and one whose layers are cached skips downloading.
	fetched bool
	// total is the size of what was fetched.
	total int64
}

func newPullReporter(out io.Writer) *pullReporter {
	terminal := false
	if f, ok := out.(*os.File); ok {
		terminal = term.IsTerminal(int(f.Fd()))
	}

	return &pullReporter{out: out, terminal: terminal}
}

// report shows one progress message.
func (r *pullReporter) report(p dicer.PullProgress) {
	stageChanged := p.Stage != r.stage
	r.stage = p.Stage
	if stageChanged {
		r.stageStart = time.Now()
	}
	if r.stage != "" && r.stage != dicer.StageResolving {
		r.fetched = true
	}
	if r.stage == dicer.StageDownloading {
		r.total = max(r.total, p.TotalBytes)
	}

	if !r.terminal {
		// Only the stages are worth a line; bytes would be a flood.
		if stageChanged {
			_, _ = fmt.Fprintf(r.out, "%s\n", stageLabel(p.Stage))
		}

		return
	}

	// Redraw on a change of stage, and otherwise no faster than the eye.
	if !stageChanged && time.Since(r.lastDraw) < progressInterval {
		return
	}
	r.lastDraw = time.Now()

	line := fmt.Sprintf("%-11s", stageLabel(p.Stage))
	if done, total := p.DownloadedBytes, p.TotalBytes; total > 0 {
		line += " " + progressBar(done, total) + " " +
			fmt.Sprintf("%4s  %s", percent(done, total), sizeOf(done, total))
		if rate := r.rate(done); rate > 0 {
			line += fmt.Sprintf("  %s/s", size(int64(rate)))
			if left := time.Duration(float64(total-done) / rate * float64(time.Second)); done < total {
				line += "  ETA " + formatDuration(left.Round(time.Second))
			}
		}
	}

	r.draw(line)
}

// rate is how fast the current stage has moved done bytes, per second.
func (r *pullReporter) rate(done int64) float64 {
	elapsed := time.Since(r.stageStart).Seconds()
	if elapsed < 0.5 || done <= 0 {
		return 0 // too soon to say
	}
	return float64(done) / elapsed
}

// progressBarWidth is how many cells the download bar spans.
const progressBarWidth = 24

// progressBar draws how far done is through total, in eighths of a cell:
// "██████████▋             ".
func progressBar(done, total int64) string {
	eighths := int(float64(min(done, total)) / float64(total) * progressBarWidth * 8)
	full, part := eighths/8, eighths%8

	bar := strings.Repeat("█", full)
	if part > 0 {
		bar += string([]rune("▏▎▍▌▋▊▉")[part-1])
	}
	pad := strings.Repeat(" ", progressBarWidth-len([]rune(bar)))

	return "▕" + bar + pad + "▏"
}

// draw rewrites the progress line, clearing whatever was longer before.
func (r *pullReporter) draw(line string) {
	_, _ = fmt.Fprintf(r.out, "\r\x1b[K%s", line)
}

// done clears the progress line, leaving the command's own output to stand
// alone.
func (r *pullReporter) done() {
	if r.terminal {
		_, _ = io.WriteString(r.out, "\r\x1b[K")
	}
}

func stageLabel(stage dicer.PullStage) string {
	switch stage {
	case dicer.StageResolving:
		return "Resolving"
	case dicer.StageDownloading:
		return "Downloading"
	case dicer.StageUnpacking:
		return "Unpacking"
	case dicer.StageConverting:
		return "Converting"
	default:
		return "Pulling"
	}
}
