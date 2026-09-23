// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"context"
	"sync"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/image/reference"
)

// A pull of one image is shared by everyone who asks for that image while it
// runs, and belongs to none of them. It runs on a context of its own, so a
// client that gives up -- a Ctrl-C, a dropped connection -- leaves the others
// waiting on it unharmed. Only when every one of them has gone is it
// cancelled.

// pull is one image's pull, and who is waiting on it.
type pull struct {
	// done is closed once img and err are set.
	done chan struct{}
	img  *dicer.Image
	err  error

	cancel   context.CancelFunc
	progress progressRelay

	// waiters counts those waiting on the pull. abandoned is set once the
	// last has gone, and the pull is being cancelled. Both are guarded by
	// Manager.pullsMu.
	waiters   int
	abandoned bool
}

// sharedPull pulls the resolved image, or joins the pull of it already
// under way, and waits for it for as long as ctx allows. onProgress hears
// how the pull goes for as long as this caller waits, and not after.
func (m *Manager) sharedPull(
	ctx context.Context, resolved *reference.ResolvedRef, onProgress ProgressFunc,
) (*dicer.Image, error) {
	digest := resolved.ManifestDigest()

	for {
		m.pullsMu.Lock()
		p := m.pulls[digest]
		if p != nil && p.abandoned {
			// A pull everyone gave up on is still removing what it
			// wrote. Another started now would write over it.
			m.pullsMu.Unlock()
			select {
			case <-p.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if p == nil {
			p = m.startPull(ctx, resolved)
		}
		p.waiters++
		listener := p.progress.add(onProgress)
		m.pullsMu.Unlock()

		select {
		case <-p.done:
			return p.img, p.err
		case <-ctx.Done():
			p.progress.remove(listener)
			m.leavePull(p)
			return nil, ctx.Err()
		}
	}
}

// startPull starts pulling the resolved image. The caller must hold
// pullsMu. The pull keeps ctx's values but not its cancellation.
func (m *Manager) startPull(ctx context.Context, resolved *reference.ResolvedRef) *pull {
	digest := resolved.ManifestDigest()

	pullCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p := &pull{done: make(chan struct{}), cancel: cancel}
	m.pulls[digest] = p

	go func() {
		defer close(p.done)
		defer cancel()

		p.img, p.err = m.ensureImageReady(pullCtx, resolved, p.progress.send)

		m.pullsMu.Lock()
		delete(m.pulls, digest)
		m.pullsMu.Unlock()
	}()

	return p
}

// leavePull stops waiting on p, and cancels it if nobody else is.
func (m *Manager) leavePull(p *pull) {
	m.pullsMu.Lock()
	defer m.pullsMu.Unlock()

	p.waiters--
	if p.waiters == 0 {
		p.abandoned = true
		p.cancel()
	}
}

// progressRelay passes a pull's progress on to everyone waiting on it.
type progressRelay struct {
	mu        sync.Mutex
	next      int
	listeners map[int]ProgressFunc
}

// add starts passing progress to f, and returns what remove takes to stop.
func (r *progressRelay) add(f ProgressFunc) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.listeners == nil {
		r.listeners = make(map[int]ProgressFunc)
	}
	r.next++
	r.listeners[r.next] = f
	return r.next
}

// remove stops passing progress to a listener. Once it returns, the
// listener is not called again.
func (r *progressRelay) remove(listener int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.listeners, listener)
}

// send passes p to every listener.
func (r *progressRelay) send(p dicer.PullProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.listeners {
		f.send(p)
	}
}
