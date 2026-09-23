// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"sync"
	"testing"

	"github.com/konradasb/dicer/internal/registry"
)

func TestDownloadCounterKeepsTheHighestCount(t *testing.T) {
	var c downloadCounter

	// Layers are fetched in parallel, so a cumulative count can arrive
	// behind one already seen. The total is the high-water mark, not the
	// last value reported.
	for _, n := range []int64{100, 500, 300, 450} {
		c.observe(n)
	}

	if got := c.total(); got != 500 {
		t.Errorf("total() = %d, want 500", got)
	}
}

func TestDownloadCounterTapForwardsEvents(t *testing.T) {
	var (
		c    downloadCounter
		seen []registry.Event
	)

	tap := c.tap(func(ev registry.Event) { seen = append(seen, ev) })

	tap(registry.Event{Phase: registry.PhaseDownloading, Downloaded: 10, Total: 20})
	tap(registry.Event{Phase: registry.PhaseUnpacking})

	if len(seen) != 2 {
		t.Fatalf("forwarded %d events, want 2", len(seen))
	}
	if seen[0].Downloaded != 10 || seen[1].Phase != registry.PhaseUnpacking {
		t.Errorf("events were altered in transit: %+v", seen)
	}
	if got := c.total(); got != 10 {
		t.Errorf("total() = %d, want 10", got)
	}
}

// types.PullProgress is optional, and a pull must be counted either way.
func TestDownloadCounterTapWithoutAListener(t *testing.T) {
	var c downloadCounter

	tap := c.tap(nil)
	tap(registry.Event{Phase: registry.PhaseDownloading, Downloaded: 42})

	if got := c.total(); got != 42 {
		t.Errorf("total() = %d, want 42", got)
	}
}

func TestDownloadCounterIsSafeUnderConcurrentEvents(t *testing.T) {
	var (
		c  downloadCounter
		wg sync.WaitGroup
	)

	for i := int64(1); i <= 50; i++ {
		wg.Go(func() { c.observe(i * 10) })
	}
	wg.Wait()

	if got := c.total(); got != 500 {
		t.Errorf("total() = %d, want 500", got)
	}
}
