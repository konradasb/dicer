// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"sync/atomic"

	"github.com/konradasb/dicer/internal/registry"
	"github.com/konradasb/dicer/internal/types"
)

// ProgressFunc receives progress as a pull runs. It may be nil, and is
// called from the goroutine doing the pull, so it should not block for long.
type ProgressFunc func(types.PullProgress)

// report passes p to f, unless f is nil.
func (f ProgressFunc) report(p types.PullProgress) {
	if f != nil {
		f(p)
	}
}

// fromRegistry adapts the registry's progress, which knows nothing of what
// happens to an image after it is fetched.
func (f ProgressFunc) fromRegistry() registry.ProgressFunc {
	if f == nil {
		return nil
	}

	return func(p registry.Progress) {
		switch p.Phase {
		case registry.PhaseDownloading:
			f(types.PullProgress{
				Stage:           types.PullStageDownloading,
				DownloadedBytes: p.Downloaded,
				TotalBytes:      p.Total,
			})
		case registry.PhaseUnpacking:
			f(types.PullProgress{Stage: types.PullStageUnpacking})
		}
	}
}

// downloadCounter keeps the highest cumulative byte count reported, since
// reports from parallel layer fetches can arrive out of order.
type downloadCounter struct {
	highest atomic.Int64
}

// tap returns a registry.ProgressFunc that totals the bytes reported, then
// forwards the progress to next, which may be nil.
func (c *downloadCounter) tap(next registry.ProgressFunc) registry.ProgressFunc {
	return func(p registry.Progress) {
		c.observe(p.Downloaded)

		if next != nil {
			next(p)
		}
	}
}

// observe records a cumulative byte count from one report.
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
