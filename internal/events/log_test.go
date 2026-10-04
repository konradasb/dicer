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

func instanceEvent(name string, action Action) Event {
	return Event{Kind: KindInstance, ID: "id-" + name, Name: name, Action: action}
}

func actions(events []Event) []Action {
	out := make([]Action, 0, len(events))
	for _, e := range events {
		out = append(out, e.Action)
	}
	return out
}

func TestRecordAndList(t *testing.T) {
	l, now := openLog(t, Config{})

	l.Record(instanceEvent("web", ActionCreated))
	*now = now.Add(time.Minute)
	l.Record(instanceEvent("web", ActionStarted))
	l.Record(Event{Kind: KindImage, Name: "nginx:1.27", Action: ActionPulled})
	l.Record(instanceEvent("db", ActionStarted))

	all := l.List(Filter{}, 0)
	if len(all) != 4 || !all[0].Time.Equal(t0) || !all[1].Time.Equal(t0.Add(time.Minute)) {
		t.Fatalf("List = %+v, want all four, stamped in order", all)
	}

	tests := []struct {
		name   string
		filter Filter
		limit  int
		want   []Action
	}{
		{"by kind", Filter{Kind: KindImage}, 0, []Action{ActionPulled}},
		{"by id", Filter{ID: "id-web"}, 0, []Action{ActionCreated, ActionStarted}},
		{"by name", Filter{Names: []string{"db"}}, 0, []Action{ActionStarted}},
		{"since", Filter{Since: t0.Add(time.Minute)}, 0, []Action{ActionStarted, ActionPulled, ActionStarted}},
		{"the last ones", Filter{}, 2, []Action{ActionPulled, ActionStarted}},
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

	e := instanceEvent("web", ActionDied)
	e.Attributes = map[string]string{"exit_code": "1"}
	l.Record(e)
	e.Attributes["exit_code"] = "changed by the recorder"

	listed := l.List(Filter{}, 0)
	listed[0].Attributes["exit_code"] = "changed by a reader"

	if got := l.List(Filter{}, 0)[0].Attributes["exit_code"]; got != "1" {
		t.Errorf("exit_code = %q, want the event as recorded", got)
	}
}

// Events outlive the daemon: a log opened again has them, and one written
// half-way when the daemon died loses only the half-written line.
func TestEventsSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{Path: path})
	l.Record(instanceEvent("web", ActionStarted))
	l.Record(instanceEvent("web", ActionDied))
	_ = l.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"time":"2026-09-22T12:0`)
	_ = f.Close()

	reopened, _ := openLog(t, Config{Path: path})
	if got := actions(reopened.List(Filter{}, 0)); !slices.Equal(got, []Action{ActionStarted, ActionDied}) {
		t.Errorf("after reopening = %v, want both events", got)
	}
}

// A line of any length is read, or skipped: none keeps the log from opening.
func TestLongEventDoesNotStopTheLogOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{Path: path})
	long := instanceEvent("web", ActionDied)
	long.Message = strings.Repeat("x", 2<<20)
	l.Record(long)
	l.Record(instanceEvent("web", ActionStarted))
	_ = l.Close()

	reopened, _ := openLog(t, Config{Path: path})
	if got := actions(reopened.List(Filter{}, 0)); !slices.Equal(got, []Action{ActionDied, ActionStarted}) {
		t.Errorf("after reopening = %v, want both events", got)
	}
}

func TestRetentionByCount(t *testing.T) {
	const maxCount = 4
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{Path: path, MaxCount: maxCount})

	for range 10 {
		l.Record(instanceEvent("web", ActionStarted))
	}
	l.Record(instanceEvent("web", ActionStopped))

	kept := l.List(Filter{}, 0)
	if len(kept) != maxCount || kept[maxCount-1].Action != ActionStopped {
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

	l.Record(instanceEvent("web", ActionStarted))
	*now = now.Add(2 * time.Hour)
	l.Record(instanceEvent("web", ActionStopped))

	if got := actions(l.List(Filter{}, 0)); !slices.Equal(got, []Action{ActionStopped}) {
		t.Errorf("kept %v, want only the event under an hour old", got)
	}
}

// A subscriber gets what happened so far and then what happens next, with
// nothing missed and nothing twice between the two.
func TestSubscribe(t *testing.T) {
	l, _ := openLog(t, Config{})
	l.Record(instanceEvent("web", ActionCreated))
	l.Record(instanceEvent("db", ActionCreated))

	history, sub := l.Subscribe(Filter{Names: []string{"web"}}, 0)
	defer sub.Close()

	l.Record(instanceEvent("db", ActionStarted))
	l.Record(instanceEvent("web", ActionStarted))

	if got := actions(history); !slices.Equal(got, []Action{ActionCreated}) {
		t.Errorf("history = %v, want web's creation", got)
	}
	select {
	case e := <-sub.Events():
		if e.Name != "web" || e.Action != ActionStarted {
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
	_, sub := l.Subscribe(Filter{}, 0)

	done := make(chan struct{})
	go func() {
		for range subscriberBuffer + 10 {
			l.Record(instanceEvent("web", ActionStarted))
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
	_, sub := l.Subscribe(Filter{}, 0)

	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-sub.Events(); open || sub.Err() != nil {
		t.Errorf("after Close: open %v, err %v; want a subscription ended cleanly", open, sub.Err())
	}
	sub.Close() // closing an ended subscription is harmless
}
