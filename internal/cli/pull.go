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

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// progressInterval is how often a terminal's progress line is redrawn. Any
// faster is wasted on a reader.
const progressInterval = 100 * time.Millisecond

// pullReporter renders a pull's progress: one updating line on a terminal,
// a line per stage elsewhere.
type pullReporter struct {
	out      io.Writer
	terminal bool

	stage      dicerdv1.PullStage
	stageStart time.Time
	lastDraw   time.Time

	// fetched is whether the pull went beyond resolving.
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
func (r *pullReporter) report(p *dicerdv1.PullImageProgress) {
	stageChanged := p.GetStage() != r.stage
	r.stage = p.GetStage()
	if stageChanged {
		r.stageStart = time.Now()
	}
	if r.stage != dicerdv1.PullStage_PULL_STAGE_UNSPECIFIED && r.stage != dicerdv1.PullStage_PULL_STAGE_RESOLVING {
		r.fetched = true
	}
	if r.stage == dicerdv1.PullStage_PULL_STAGE_DOWNLOADING {
		r.total = max(r.total, p.GetTotalBytes())
	}

	if !r.terminal {
		// Only the stages are worth a line; bytes would be a flood.
		if stageChanged {
			_, _ = fmt.Fprintf(r.out, "%s\n", stageLabel(p.GetStage()))
		}

		return
	}

	// Redraw on a change of stage, and otherwise no faster than the eye.
	if !stageChanged && time.Since(r.lastDraw) < progressInterval {
		return
	}
	r.lastDraw = time.Now()

	line := fmt.Sprintf("%-11s", stageLabel(p.GetStage()))
	if done, total := p.GetDownloadedBytes(), p.GetTotalBytes(); total > 0 {
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

// stageLabel is what a progress line calls a stage: "Downloading".
func stageLabel(stage dicerdv1.PullStage) string {
	if stage == dicerdv1.PullStage_PULL_STAGE_UNSPECIFIED {
		return "Pulling"
	}
	return capitalize(enumName(stage))
}
