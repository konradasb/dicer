// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// recordedEvents remembers the events it is given.
type recordedEvents []types.Event

func (r *recordedEvents) Record(e types.Event) { *r = append(*r, e) }

func (r *recordedEvents) actions() []types.EventAction {
	out := make([]types.EventAction, 0, len(*r))
	for _, e := range *r {
		out = append(out, e.Action)
	}
	return out
}

// newRecordingManager returns a test manager whose events are recorded.
func newRecordingManager(t *testing.T) (*Manager, *mockRegistryClient, *recordedEvents) {
	t.Helper()

	m, mock := newPruneTestManager(t)
	recorded := &recordedEvents{}
	m.events = recorded
	return m, mock, recorded
}

// A pull is recorded once, when it goes to a registry; asking for an image
// already held is not a pull.
func TestPullIsRecorded(t *testing.T) {
	m, mock, recorded := newRecordingManager(t)

	pullTestImage(t, m, mock, "docker.io/library/nginx:1.27", "sha256:aaa")
	pullTestImage(t, m, mock, "docker.io/library/nginx:1.27", "sha256:aaa")

	if got := recorded.actions(); !slices.Equal(got, []types.EventAction{types.ActionPulled}) {
		t.Fatalf("recorded %v, want one pull", got)
	}
	e := (*recorded)[0]
	if e.Message == "" {
		t.Errorf("pulled has no description")
	}
	if e.Kind != types.KindImage || e.Name != "docker.io/library/nginx:1.27" || e.Attributes["digest"] != "sha256:aaa" {
		t.Errorf("pulled = %+v, want the image by its reference, with its digest", e)
	}
}

// Deleting, pruning and collecting all remove an image; the events say which.
func TestRemovalsAreRecorded(t *testing.T) {
	m, mock, recorded := newRecordingManager(t)
	pullTestImage(t, m, mock, "docker.io/library/a:1", "sha256:a")
	pullTestImage(t, m, mock, "docker.io/library/b:1", "sha256:b")
	pullTestImage(t, m, mock, "docker.io/library/c:1", "sha256:c")
	setLastUsed(t, m, "sha256:c", gcNow.Add(-30*24*time.Hour))
	*recorded = nil

	if err := m.Delete("docker.io/library/a:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CollectGarbage(GCPolicy{MaxUnusedAge: 24 * time.Hour},
		map[string]struct{}{"sha256:b": {}}, gcNow); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Prune(nil); err != nil {
		t.Fatal(err)
	}

	want := []struct {
		name   string
		action types.EventAction
		attr   string
		value  string
	}{
		{"docker.io/library/a:1", types.ActionDeleted, "by", "user"},
		{"docker.io/library/c:1", types.ActionCollected, "reason", "unused"},
		{"docker.io/library/b:1", types.ActionDeleted, "by", "prune"},
	}
	if len(*recorded) != len(want) {
		t.Fatalf("recorded %v, want %d events", recorded.actions(), len(want))
	}
	for i, w := range want {
		e := (*recorded)[i]
		if e.Message == "" {
			t.Errorf("event %d has no description: %+v", i, e)
		}
		if e.Name != w.name || e.Action != w.action || e.Attributes[w.attr] != w.value {
			t.Errorf("event %d = %+v, want %s %s with %s=%s", i, e, w.name, w.action, w.attr, w.value)
		}
	}
}

func TestGCMessage(t *testing.T) {
	img := &types.Image{
		Name: "docker.io/library/alpine:3", Digest: "sha256:1cfa4e2b09e1aaaaaaaaaaaa", SizeBytes: 5 << 20,
		LastUsedAt: time.Date(2026, 8, 23, 10, 0, 0, 0, time.Local),
	}
	p := GCPolicy{MaxUnusedAge: 30 * 24 * time.Hour, MaxSize: 10 << 30}

	want := "Garbage-collected image docker.io/library/alpine:3 (sha256:1cfa4e2b09e1): " +
		"unused since 2026-08-23 10:00:00, longer than gc_max_unused_age 30d; 5 MiB boot disk removed"
	if got := gcMessage(p, img, GCReasonUnused); got != want {
		t.Errorf("gcMessage =\n%q\nwant\n%q", got, want)
	}
	if got := gcMessage(p, img, GCReasonSize); !strings.Contains(got, "over gc_max_size 10 GiB") {
		t.Errorf("gcMessage = %q, want the size limit", got)
	}
}

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * 24 * time.Hour: "30d",
		36 * time.Hour:      "36h",
		90 * time.Minute:    "1h30m",
	} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}
