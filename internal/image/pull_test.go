// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/registry"
	"github.com/konradasb/dicer/internal/types"
)

// newBlockingManager returns a manager whose pulls wait for release, and
// report on started when they begin and on cancelled if their context ends
// first.
func newBlockingManager(t *testing.T) (m *Manager, started, cancelled chan struct{}, release chan struct{}) {
	t.Helper()

	m, err := NewManager(Config{DataDir: t.TempDir(), Logger: discardLogger})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	started = make(chan struct{}, 1)
	cancelled = make(chan struct{}, 1)
	release = make(chan struct{})

	mock := &mockRegistryClient{}
	next := mock.PullAndExport
	mock.pullAndExportFunc = func(ctx context.Context, imageRef, digest, exportDir string) (*registry.PullResult, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			cancelled <- struct{}{}
			return nil, ctx.Err()
		}
		mock.pullAndExportFunc = nil
		return next(ctx, imageRef, digest, exportDir, nil)
	}
	m.registry = mock
	m.packer = &mockPacker{}
	return m, started, cancelled, release
}

func TestPullSurvivesAWaiterGivingUp(t *testing.T) {
	m, started, _, release := newBlockingManager(t)

	first, cancelFirst := context.WithCancel(t.Context())
	firstErr := make(chan error, 1)
	go func() {
		_, err := m.Pull(first, "alpine:latest", func(types.PullProgress) {})
		firstErr <- err
	}()
	<-started

	second := make(chan error, 1)
	go func() {
		_, err := m.Pull(t.Context(), "alpine:latest", nil)
		second <- err
	}()
	waitForWaiters(t, m, 2)

	// The first caller, who started the pull, gives up.
	cancelFirst()
	if err := <-firstErr; !errors.Is(err, context.Canceled) {
		t.Errorf("the caller who gave up got %v, want context.Canceled", err)
	}

	close(release)
	if err := <-second; err != nil {
		t.Errorf("the caller still waiting got %v, want the image", err)
	}
}

func TestPullEveryoneAbandonsIsCancelled(t *testing.T) {
	m, started, cancelled, _ := newBlockingManager(t)

	ctx, cancel := context.WithCancel(t.Context())
	errs := make(chan error, 1)
	go func() {
		_, err := m.Pull(ctx, "alpine:latest", nil)
		errs <- err
	}()
	<-started

	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Errorf("Pull = %v, want context.Canceled", err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("a pull nobody waits for was not cancelled")
	}
}

func TestProgressRelayStopsAtRemove(t *testing.T) {
	var r progressRelay
	var got []types.PullStage
	id := r.add(func(p types.PullProgress) { got = append(got, p.Stage) })

	r.send(types.PullProgress{Stage: types.StageDownloading})
	r.remove(id)
	r.send(types.PullProgress{Stage: types.StageConverting})

	if len(got) != 1 || got[0] != types.StageDownloading {
		t.Errorf("listener heard %v, want only what was sent before it was removed", got)
	}
}

// waitForWaiters waits until n callers are waiting on the manager's one pull.
func waitForWaiters(t *testing.T, m *Manager, n int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.pullsMu.Lock()
		waiting := 0
		for _, p := range m.pulls {
			waiting += p.waiters
		}
		m.pullsMu.Unlock()
		if waiting == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("never saw %d callers waiting on the pull", n)
}
