// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package events

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
)

var t0 = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// openLog opens a log in a fresh directory, on a clock the test moves.
func openLog(t *testing.T, cfg Config) (*Log, *time.Time) {
	t.Helper()

	if cfg.Path == "" {
		cfg.Path = filepath.Join(t.TempDir(), "events.jsonl")
	}
	cfg.Logger = slog.New(slog.DiscardHandler)

	l, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	now := t0
	l.now = func() time.Time { return now }
	return l, &now
}

func instanceEvent(name string, action dicer.EventAction) dicer.Event {
	return dicer.Event{Kind: dicer.KindInstance, ID: "id-" + name, Name: name, Action: action}
}

func actions(events []dicer.Event) []dicer.EventAction {
	out := make([]dicer.EventAction, 0, len(events))
	for _, e := range events {
		out = append(out, e.Action)
	}
	return out
}

func TestRecordAndList(t *testing.T) {
	l, now := openLog(t, Config{})

	l.Record(instanceEvent("web", dicer.ActionCreated))
	*now = now.Add(time.Minute)
	l.Record(instanceEvent("web", dicer.ActionStarted))
	l.Record(dicer.Event{Kind: dicer.KindImage, Name: "nginx:1.27", Action: dicer.ActionPulled})
	l.Record(instanceEvent("db", dicer.ActionStarted))

	all := l.List(dicer.EventFilter{}, 0)
	if len(all) != 4 || !all[0].Time.Equal(t0) || !all[1].Time.Equal(t0.Add(time.Minute)) {
		t.Fatalf("List = %+v, want all four, stamped in order", all)
	}

	tests := []struct {
		name   string
		filter dicer.EventFilter
		limit  int
		want   []dicer.EventAction
	}{
		{"by kind", dicer.EventFilter{Kind: dicer.KindImage}, 0, []dicer.EventAction{dicer.ActionPulled}},
		{"by id", dicer.EventFilter{ID: "id-web"}, 0, []dicer.EventAction{dicer.ActionCreated, dicer.ActionStarted}},
		{"by name", dicer.EventFilter{Names: []string{"db"}}, 0, []dicer.EventAction{dicer.ActionStarted}},
		{"since", dicer.EventFilter{Since: t0.Add(time.Minute)}, 0, []dicer.EventAction{dicer.ActionStarted, dicer.ActionPulled, dicer.ActionStarted}},
		{"the last ones", dicer.EventFilter{}, 2, []dicer.EventAction{dicer.ActionPulled, dicer.ActionStarted}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := actions(l.List(tt.filter, tt.limit)); !slices.Equal(got, tt.want) {
				t.Errorf("List = %v, want %v", got, tt.want)
			}
		})
	}
}

// What a reader is handed is its own: changing it changes nothing kept.
func TestEventsAreCopied(t *testing.T) {
	l, _ := openLog(t, Config{})

	e := instanceEvent("web", dicer.ActionDied)
	e.Attributes = map[string]string{"exit_code": "1"}
	l.Record(e)
	e.Attributes["exit_code"] = "changed by the recorder"

	listed := l.List(dicer.EventFilter{}, 0)
	listed[0].Attributes["exit_code"] = "changed by a reader"

	if got := l.List(dicer.EventFilter{}, 0)[0].Attributes["exit_code"]; got != "1" {
		t.Errorf("exit_code = %q, want the event as recorded", got)
	}
}

// Events outlive the daemon: a log opened again has them, and one written
// half-way when the daemon died loses only the half-written line.
func TestEventsSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{Path: path})
	l.Record(instanceEvent("web", dicer.ActionStarted))
	l.Record(instanceEvent("web", dicer.ActionDied))
	_ = l.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"time":"2026-09-22T12:0`)
	_ = f.Close()

	reopened, _ := openLog(t, Config{Path: path})
	if got := actions(reopened.List(dicer.EventFilter{}, 0)); !slices.Equal(got, []dicer.EventAction{dicer.ActionStarted, dicer.ActionDied}) {
		t.Errorf("after reopening = %v, want both events", got)
	}
}

// A line of any length is read, or skipped: none keeps the log from opening.
func TestLongEventDoesNotStopTheLogOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{Path: path})
	long := instanceEvent("web", dicer.ActionDied)
	long.Message = strings.Repeat("x", 2<<20)
	l.Record(long)
	l.Record(instanceEvent("web", dicer.ActionStarted))
	_ = l.Close()

	reopened, _ := openLog(t, Config{Path: path})
	if got := actions(reopened.List(dicer.EventFilter{}, 0)); !slices.Equal(got, []dicer.EventAction{dicer.ActionDied, dicer.ActionStarted}) {
		t.Errorf("after reopening = %v, want both events", got)
	}
}

func TestRetentionByCount(t *testing.T) {
	const maxCount = 4
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{Path: path, MaxCount: maxCount})

	for range 10 {
		l.Record(instanceEvent("web", dicer.ActionStarted))
	}
	l.Record(instanceEvent("web", dicer.ActionStopped))

	kept := l.List(dicer.EventFilter{}, 0)
	if len(kept) != maxCount || kept[maxCount-1].Action != dicer.ActionStopped {
		t.Errorf("kept %d events ending in %v, want the last 4", len(kept), kept[len(kept)-1].Action)
	}

	// The file is compacted as it grows, not left to grow for ever.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if limit := maxCount + maxCount/4; strings.Count(string(data), "\n") > limit {
		t.Errorf("the file holds %d events, want at most %d", strings.Count(string(data), "\n"), limit)
	}
}

func TestRetentionByAge(t *testing.T) {
	l, now := openLog(t, Config{MaxAge: time.Hour})

	l.Record(instanceEvent("web", dicer.ActionStarted))
	*now = now.Add(2 * time.Hour)
	l.Record(instanceEvent("web", dicer.ActionStopped))

	if got := actions(l.List(dicer.EventFilter{}, 0)); !slices.Equal(got, []dicer.EventAction{dicer.ActionStopped}) {
		t.Errorf("kept %v, want only the event under an hour old", got)
	}
}

// A subscriber gets what happened so far and then what happens next, with
// nothing missed and nothing twice between the two.
func TestSubscribe(t *testing.T) {
	l, _ := openLog(t, Config{})
	l.Record(instanceEvent("web", dicer.ActionCreated))
	l.Record(instanceEvent("db", dicer.ActionCreated))

	history, sub := l.Subscribe(dicer.EventFilter{Names: []string{"web"}}, 0)
	defer sub.Close()

	l.Record(instanceEvent("db", dicer.ActionStarted))
	l.Record(instanceEvent("web", dicer.ActionStarted))

	if got := actions(history); !slices.Equal(got, []dicer.EventAction{dicer.ActionCreated}) {
		t.Errorf("history = %v, want web's creation", got)
	}
	select {
	case e := <-sub.Events():
		if e.Name != "web" || e.Action != dicer.ActionStarted {
			t.Errorf("followed %+v, want web started", e)
		}
	case <-time.After(time.Second):
		t.Fatal("the new event did not arrive")
	}
	select {
	case e := <-sub.Events():
		t.Errorf("followed %+v too, want only web's events", e)
	default:
	}
}

// A subscriber that does not keep up is cut off, rather than holding up the
// lifecycle that records the events.
func TestSlowSubscriberIsCutOff(t *testing.T) {
	l, _ := openLog(t, Config{})
	_, sub := l.Subscribe(dicer.EventFilter{}, 0)

	done := make(chan struct{})
	go func() {
		for range subscriberBuffer + 10 {
			l.Record(instanceEvent("web", dicer.ActionStarted))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record waited on a subscriber that did not read")
	}

	for range sub.Events() {
		// Drain what was buffered; the channel is then closed.
	}
	if !errors.Is(sub.Err(), ErrFellBehind) {
		t.Errorf("Err = %v, want ErrFellBehind", sub.Err())
	}
}

func TestCloseEndsSubscriptions(t *testing.T) {
	l, _ := openLog(t, Config{})
	_, sub := l.Subscribe(dicer.EventFilter{}, 0)

	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-sub.Events(); open || sub.Err() != nil {
		t.Errorf("after Close: open %v, err %v; want a subscription ended cleanly", open, sub.Err())
	}
	sub.Close() // closing an ended subscription is harmless
}
