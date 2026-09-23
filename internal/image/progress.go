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

func (f ProgressFunc) send(p types.PullProgress) {
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
			f(types.PullProgress{
				Stage:           types.StageDownloading,
				DownloadedBytes: ev.Downloaded,
				TotalBytes:      ev.Total,
			})
		case registry.PhaseUnpacking:
			f(types.PullProgress{Stage: types.StageUnpacking})
		}
	}
}

// downloadCounter keeps the highest cumulative byte count reported, since
// events from parallel layer fetches can arrive out of order.
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
