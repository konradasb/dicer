// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"sync/atomic"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/registry"
)

// ProgressFunc receives progress as a pull runs. It may be nil, and is
// called from the goroutine doing the pull, so it should not block for long.
type ProgressFunc func(dicer.PullProgress)

func (f ProgressFunc) send(p dicer.PullProgress) {
	if f != nil {
		f(p)
	}
}

// fromRegistry adapts the registry's events, which know nothing of what
// happens to an image after it is fetched.
func (f ProgressFunc) fromRegistry() registry.EventFunc {
	if f == nil {
		return nil
	}

	return func(ev registry.Event) {
		switch ev.Phase {
		case registry.PhaseDownloading:
			f(dicer.PullProgress{
				Stage:           dicer.StageDownloading,
				DownloadedBytes: ev.Downloaded,
				TotalBytes:      ev.Total,
			})
		case registry.PhaseUnpacking:
			f(dicer.PullProgress{Stage: dicer.StageUnpacking})
		}
	}
}

// downloadCounter totals the compressed bytes a pull fetched.
//
// The registry reports a cumulative count on every event, and fetches layers
// in parallel, so events can arrive out of order; keeping the highest count
// seen is what makes the total meaningful rather than whichever event
// happened to be last.
type downloadCounter struct {
	highest atomic.Int64
}

// tap returns an EventFunc that totals the bytes reported and then forwards
// to next, which may be nil.
func (c *downloadCounter) tap(next registry.EventFunc) registry.EventFunc {
	return func(ev registry.Event) {
		c.observe(ev.Downloaded)

		if next != nil {
			next(ev)
		}
	}
}

// observe records a cumulative byte count from one event.
func (c *downloadCounter) observe(cumulative int64) {
	for {
		highest := c.highest.Load()
		if cumulative <= highest || c.highest.CompareAndSwap(highest, cumulative) {
			return
		}
	}
}

// total returns the bytes downloaded.
func (c *downloadCounter) total() int64 { return c.highest.Load() }
