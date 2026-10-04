// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/atomicfile"
)

var t0 = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// openLog opens a log in a fresh directory, on a clock the test moves.
func openLog(t *testing.T, cfg Config) (*Log, *time.Time) {
	t.Helper()

	if cfg.File == "" {
		cfg.File = filepath.Join(t.TempDir(), "events.jsonl")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	l, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	now := t0
	l.now = func() time.Time { return now }

	return l, &now
}

// instanceEvent returns an event about the instance name.
func instanceEvent(name string, action Action) Event {
	return Event{Kind: KindInstance, ID: "id-" + name, Name: name, Action: action}
}

// actions returns each event's action, in order.
func actions(events []Event) []Action {
	out := make([]Action, 0, len(events))
	for _, e := range events {
		out = append(out, e.Action)
	}

	return out
}

// TestRecordedEventsAreListedByFilter checks that List returns the recorded
// events, stamped in order, that a filter and limit pick.
func TestRecordedEventsAreListedByFilter(t *testing.T) {
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
		{"since", Filter{Since: t0.Add(time.Minute)}, 0,
			[]Action{ActionStarted, ActionPulled, ActionStarted}},
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

// TestEventsAreCopied checks that what a reader is handed is its own:
// changing it changes nothing kept.
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

// TestEventsSurviveAReopen checks that events outlive the daemon: a log
// opened again has them, and one written half-way when the daemon died loses
// only the half-written line.
func TestEventsSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{File: path})
	l.Record(instanceEvent("web", ActionStarted))
	l.Record(instanceEvent("web", ActionDied))
	_ = l.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"time":"2026-09-22T12:0`)
	_ = f.Close()

	reopened, _ := openLog(t, Config{File: path})
	want := []Action{ActionStarted, ActionDied}
	if got := actions(reopened.List(Filter{}, 0)); !slices.Equal(got, want) {
		t.Errorf("after reopening = %v, want both events", got)
	}
}

// TestLongEventDoesNotStopTheLogOpening checks that a line of any length is
// read, or skipped: none keeps the log from opening.
func TestLongEventDoesNotStopTheLogOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{File: path})
	long := instanceEvent("web", ActionDied)
	long.Message = strings.Repeat("x", 2<<20)
	l.Record(long)
	l.Record(instanceEvent("web", ActionStarted))
	_ = l.Close()

	reopened, _ := openLog(t, Config{File: path})
	if got := actions(reopened.List(Filter{}, 0)); !slices.Equal(got, []Action{ActionDied, ActionStarted}) {
		t.Errorf("after reopening = %v, want both events", got)
	}
}

// TestOpenMakesTheDirectory checks that the file's directory is made, so
// that nothing need be prepared for it.
func TestOpenMakesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "events.jsonl")
	l, _ := openLog(t, Config{File: path})
	l.Record(instanceEvent("web", ActionCreated))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the events file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != fileMode {
		t.Errorf("mode = %o, want %o", perm, fileMode)
	}
}

// TestOnlyTheMostRecentEventsAreKept checks that only the most recent events
// are kept, and that the file is compacted as it grows rather than left to
// grow for ever.
func TestOnlyTheMostRecentEventsAreKept(t *testing.T) {
	const maxCount = 4

	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{File: path, MaxCount: maxCount})

	for range 10 {
		l.Record(instanceEvent("web", ActionStarted))
	}
	l.Record(instanceEvent("web", ActionStopped))

	kept := l.List(Filter{}, 0)
	if len(kept) != maxCount {
		t.Fatalf("kept %d events, want the last %d", len(kept), maxCount)
	}
	if last := kept[len(kept)-1].Action; last != ActionStopped {
		t.Errorf("kept events ending in %v, want the last recorded", last)
	}

	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if limit := maxCount + maxCount/4; strings.Count(string(data), "\n") > limit {
		t.Errorf("the file holds %d events, want at most %d", strings.Count(string(data), "\n"), limit)
	}
}

// TestEventsPastMaxAgeAreDropped checks that events older than MaxAge are
// dropped.
func TestEventsPastMaxAgeAreDropped(t *testing.T) {
	l, now := openLog(t, Config{MaxAge: time.Hour})

	l.Record(instanceEvent("web", ActionStarted))
	*now = now.Add(2 * time.Hour)
	l.Record(instanceEvent("web", ActionStopped))

	if got := actions(l.List(Filter{}, 0)); !slices.Equal(got, []Action{ActionStopped}) {
		t.Errorf("kept %v, want only the event under an hour old", got)
	}
}

// TestCloseWritesPendingEvents checks that Close returns only once every event
// recorded is in the file, in the order recorded.
func TestCloseWritesPendingEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := openLog(t, Config{File: path})

	want := make([]string, 0, 100)
	for i := range 100 {
		name := fmt.Sprintf("web-%d", i)
		l.Record(instanceEvent(name, ActionCreated))
		want = append(want, name)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	if got := namesInFile(t, path); !slices.Equal(got, want) {
		t.Errorf("the file holds %v, want %v", got, want)
	}
}

// TestRecordDoesNotWaitForTheFile checks that Record returns while the file
// is slow to write, and that the events still reach the file in order.
func TestRecordDoesNotWaitForTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file := newGatedFile()
	l := openGatedLog(t, Config{File: path}, file, writeBuffer)

	recorded := make(chan struct{})
	go func() {
		for _, name := range []string{"web-a", "web-b", "web-c"} {
			l.Record(instanceEvent(name, ActionCreated))
		}
		close(recorded)
	}()

	select {
	case <-recorded:
	case <-time.After(5 * time.Second):
		t.Fatal("Record waited for the file")
	}
	if got := l.List(Filter{}, 0); len(got) != 3 {
		t.Errorf("listed %d events while the file was held up, want 3", len(got))
	}

	file.release()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := namesInFile(t, path), []string{"web-a", "web-b", "web-c"}; !slices.Equal(got, want) {
		t.Errorf("the file holds %v, want %v", got, want)
	}
}

// TestEventsDroppedFromAFullQueueAreWrittenOnceTheWriterCatchesUp checks that
// events recorded while the queue to the file is full are still kept, and are
// in the file, in order, as soon as the writer has caught up: a daemon that
// dies before Close loses none of them.
func TestEventsDroppedFromAFullQueueAreWrittenOnceTheWriterCatchesUp(t *testing.T) {
	const buffer = 2

	path := filepath.Join(t.TempDir(), "events.jsonl")
	file := newGatedFile()
	l := openGatedLog(t, Config{File: path}, file, buffer)

	want := make([]string, 0, 10)
	for i := range 10 {
		name := fmt.Sprintf("web-%d", i)
		l.Record(instanceEvent(name, ActionCreated))
		want = append(want, name)
	}

	l.mu.Lock()
	dropped := l.dropped
	l.mu.Unlock()
	if dropped == 0 {
		t.Errorf("dropped no events, want some past a queue of %d", buffer)
	}

	file.release()
	waitFor(t, func() bool { return slices.Equal(tryNamesInFile(path), want) })

	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got := namesInFile(t, path); !slices.Equal(got, want) {
		t.Errorf("after Close the file holds %v, want %v", got, want)
	}
}

// gatedFile opens the events file with writes that wait until released.
type gatedFile struct {
	released chan struct{}
	once     sync.Once
}

func newGatedFile() *gatedFile {
	return &gatedFile{released: make(chan struct{})}
}

// release lets the writes through, and every one after them.
func (g *gatedFile) release() { g.once.Do(func() { close(g.released) }) }

// open opens path for appending, through the gate.
func (g *gatedFile) open(path string) (io.WriteCloser, error) {
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}

	return gatedWriter{WriteCloser: f, released: g.released}, nil
}

// gatedWriter is a file whose writes wait until released is closed.
type gatedWriter struct {
	io.WriteCloser
	released <-chan struct{}
}

func (w gatedWriter) Write(p []byte) (int, error) {
	<-w.released
	return w.WriteCloser.Write(p)
}

// openGatedLog opens a log whose file is written through g, with room for
// buffer events to wait to be written.
func openGatedLog(t *testing.T, cfg Config, g *gatedFile, buffer int) *Log {
	t.Helper()

	l := openLogWith(t, cfg, &eventsFile{open: g.open, replace: atomicfile.Write, now: time.Now}, buffer)
	t.Cleanup(g.release)

	return l
}

// namesInFile returns the name of each event in the file at path, in order.
func namesInFile(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for line := range bytes.Lines(data) {
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("an unreadable line %q: %v", line, err)
		}
		names = append(names, e.Name)
	}

	return names
}

// tryNamesInFile is namesInFile for a file still being written: it returns
// nil if the file cannot be read whole.
func tryNamesInFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var names []string
	for line := range bytes.Lines(data) {
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil
		}
		names = append(names, e.Name)
	}

	return names
}

// TestADroppedEventOutlivesAFailedCompaction checks that events dropped from
// a full queue are still written on Close when the compaction before it
// failed: only a compaction that rewrote the file counts them as written.
func TestADroppedEventOutlivesAFailedCompaction(t *testing.T) {
	const maxCount, buffer = 4, 5

	path := filepath.Join(t.TempDir(), "events.jsonl")
	file := newGatedFile()
	replace := &flakyReplace{failOn: map[int]bool{2: true}}
	l := openLogWith(t, Config{File: path, MaxCount: maxCount},
		&eventsFile{open: file.open, replace: replace.replace, now: newFakeClock().now}, buffer)
	t.Cleanup(file.release)

	// The writer takes the first event and waits on the file; the queue
	// then takes five more, and the rest are dropped. Writing the sixth
	// grows the file past its limit, and that compaction fails.
	l.Record(instanceEvent("web-1", ActionCreated))
	waitForQueueEmpty(t, l)
	for i := 2; i <= 12; i++ {
		l.Record(instanceEvent(fmt.Sprintf("web-%d", i), ActionCreated))
	}

	file.release()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if calls := replace.count(); calls != 3 {
		t.Errorf("the file was replaced %d times, want 3: on open, the failed compaction and on Close", calls)
	}
	want := []string{"web-9", "web-10", "web-11", "web-12"}
	if got := namesInFile(t, path); !slices.Equal(got, want) {
		t.Errorf("the file holds %v, want %v", got, want)
	}
}

// TestAFailedWriteIsRepairedByRewritingTheFile checks that a write that fails
// part-way through a line is neither lost nor left to spoil the next line:
// the file is rewritten whole.
func TestAFailedWriteIsRepairedByRewritingTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file := &failingFile{failWrites: map[int]bool{2: true}}
	l := openLogWith(t, Config{File: path},
		&eventsFile{open: file.open, replace: atomicfile.Write, now: time.Now}, writeBuffer)

	for _, name := range []string{"web-a", "web-b", "web-c"} {
		l.Record(instanceEvent(name, ActionCreated))
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	if got, want := namesInFile(t, path), []string{"web-a", "web-b", "web-c"}; !slices.Equal(got, want) {
		t.Errorf("the file holds %v, want %v", got, want)
	}
}

// TestAFailedCompactionIsRetriedAfterADelay checks that a file that cannot be
// compacted is not tried again for every event, only once the retry delay
// has passed, and once more on Close.
func TestAFailedCompactionIsRetriedAfterADelay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file := &failingFile{failAll: true}
	replace := &flakyReplace{failFrom: 2}
	clock := newFakeClock()
	l := openLogWith(t, Config{File: path},
		&eventsFile{open: file.open, replace: replace.replace, now: clock.now}, writeBuffer)

	// The first event's write fails, and so does the compaction after it.
	l.Record(instanceEvent("web-1", ActionCreated))
	waitFor(t, func() bool { return replace.count() == 2 })

	for i := 2; i <= 5; i++ {
		l.Record(instanceEvent(fmt.Sprintf("web-%d", i), ActionCreated))
	}
	clock.advance(compactionRetryDelay)
	l.Record(instanceEvent("web-6", ActionCreated))
	_ = l.Close()

	if calls := replace.count(); calls != 4 {
		t.Errorf("the file was replaced %d times, want 4: on open, after the failed write, "+
			"once after the delay and on Close", calls)
	}
}

// TestCloseReturnsAFailedCompaction checks that Close says when the file
// could not be compacted with the events it lacks, rather than only logging
// it, so that the daemon reports the loss.
func TestCloseReturnsAFailedCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file := &failingFile{failAll: true}
	replace := &flakyReplace{failFrom: 2}
	l := openLogWith(t, Config{File: path},
		&eventsFile{open: file.open, replace: replace.replace, now: newFakeClock().now}, writeBuffer)

	l.Record(instanceEvent("web-1", ActionCreated))

	if err := l.Close(); !errors.Is(err, errDiskFull) {
		t.Errorf("Close = %v, want the compaction's error", err)
	}
	if err := l.Close(); !errors.Is(err, errDiskFull) {
		t.Errorf("Close again = %v, want the same error", err)
	}
}

// TestDroppingIsWarnedOfOnce checks that a run of events dropped from a full
// queue is warned of once, not once an event.
func TestDroppingIsWarnedOfOnce(t *testing.T) {
	var log syncBuffer
	file := newGatedFile()
	l := openLogWith(t, Config{
		File:   filepath.Join(t.TempDir(), "events.jsonl"),
		Logger: slog.New(slog.NewTextHandler(&log, nil)),
	}, &eventsFile{open: file.open, replace: atomicfile.Write, now: time.Now}, 2)
	t.Cleanup(file.release)

	l.Record(instanceEvent("web-0", ActionCreated))
	waitForQueueEmpty(t, l)
	for i := range 10 {
		l.Record(instanceEvent(fmt.Sprintf("web-%d", i+1), ActionCreated))
	}

	if n := strings.Count(log.String(), "faster than they are written"); n != 1 {
		t.Errorf("warned %d times of dropped events, want once:\n%s", n, log.String())
	}
}

// TestRecordingAfterCloseIsWarnedOfOnce checks that events recorded after
// Close are kept in memory, and warned of once rather than once each.
func TestRecordingAfterCloseIsWarnedOfOnce(t *testing.T) {
	var log syncBuffer
	l, _ := openLog(t, Config{Logger: slog.New(slog.NewTextHandler(&log, nil))})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	l.Record(instanceEvent("web-a", ActionCreated))
	l.Record(instanceEvent("web-b", ActionCreated))

	if got := len(l.List(Filter{}, 0)); got != 2 {
		t.Errorf("listed %d events, want both kept in memory", got)
	}
	if n := strings.Count(log.String(), "after the event log closed"); n != 1 {
		t.Errorf("warned %d times of events recorded after Close, want once:\n%s", n, log.String())
	}
}

// TestEventsACompactionWroteAreNotWrittenAgain checks that events still
// queued when a compaction writes them are skipped, not appended twice.
func TestEventsACompactionWroteAreNotWrittenAgain(t *testing.T) {
	const maxCount = 4

	path := filepath.Join(t.TempDir(), "events.jsonl")
	file := newGatedFile()
	l := openGatedLog(t, Config{File: path, MaxCount: maxCount}, file, writeBuffer)

	// The sixth write compacts the file with all ten events recorded, four
	// of them still queued.
	l.Record(instanceEvent("web-1", ActionCreated))
	waitForQueueEmpty(t, l)
	for i := 2; i <= 10; i++ {
		l.Record(instanceEvent(fmt.Sprintf("web-%d", i), ActionCreated))
	}

	file.release()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"web-7", "web-8", "web-9", "web-10"}
	if got := namesInFile(t, path); !slices.Equal(got, want) {
		t.Errorf("the file holds %v, want %v", got, want)
	}
}

// TestAnEventDroppedDuringACompactionIsWrittenWhileIdle checks that an event
// dropped while a compaction runs, behind queued events that compaction
// wrote, is in the file once the writer has skipped them, not only after
// Close.
func TestAnEventDroppedDuringACompactionIsWrittenWhileIdle(t *testing.T) {
	const maxCount, buffer = 1, 2

	path := filepath.Join(t.TempDir(), "events.jsonl")
	write, replace := newGate(2), newGate(2)
	l := openLogWith(t, Config{File: path, MaxCount: maxCount},
		&eventsFile{open: write.open, replace: replace.replace, now: time.Now}, buffer)
	t.Cleanup(write.release)
	t.Cleanup(replace.release)

	// The second write waits while the queue fills with the third and
	// fourth events. Once written, it grows the file past its limit, and
	// the compaction snapshots all four, then waits while the fifth is
	// dropped from the full queue.
	l.Record(instanceEvent("web-1", ActionCreated))
	l.Record(instanceEvent("web-2", ActionCreated))
	<-write.reached
	l.Record(instanceEvent("web-3", ActionCreated))
	l.Record(instanceEvent("web-4", ActionCreated))
	write.release()
	<-replace.reached
	l.Record(instanceEvent("web-5", ActionCreated))

	l.mu.Lock()
	dropped := l.dropped
	l.mu.Unlock()
	if dropped != 1 {
		t.Fatalf("dropped %d events, want the fifth", dropped)
	}

	replace.release()
	want := []string{"web-5"}
	waitFor(t, func() bool { return slices.Equal(tryNamesInFile(path), want) })
}

// gate holds up the nth call through it until released, and says when that
// call has reached it.
type gate struct {
	n        int64
	calls    atomic.Int64
	reached  chan struct{}
	released chan struct{}
	once     sync.Once
}

func newGate(n int) *gate {
	return &gate{n: int64(n), reached: make(chan struct{}), released: make(chan struct{})}
}

// release lets the held call through, and every one after it.
func (g *gate) release() { g.once.Do(func() { close(g.released) }) }

// wait counts a call, holding up the nth until released.
func (g *gate) wait() {
	if g.calls.Add(1) != g.n {
		return
	}
	close(g.reached)
	<-g.released
}

// open opens path for appending, with writes through the gate.
func (g *gate) open(path string) (io.WriteCloser, error) {
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}

	return gateWriter{WriteCloser: f, gate: g}, nil
}

// replace replaces the file as atomicfile.Write does, through the gate.
func (g *gate) replace(path string, data []byte, perm os.FileMode) error {
	g.wait()

	return atomicfile.Write(path, data, perm)
}

// gateWriter is a file whose writes go through a gate.
type gateWriter struct {
	io.WriteCloser
	gate *gate
}

func (w gateWriter) Write(p []byte) (int, error) {
	w.gate.wait()

	return w.WriteCloser.Write(p)
}

// openLogWith opens a log whose file is written through f, with room for buffer
// events to wait to be written.
func openLogWith(t *testing.T, cfg Config, f *eventsFile, buffer int) *Log {
	t.Helper()

	l, err := open(cfg, f, buffer)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	return l
}

// waitForQueueEmpty waits until the writer has taken every queued event.
func waitForQueueEmpty(t *testing.T, l *Log) {
	t.Helper()

	waitFor(t, func() bool { return len(l.writes) == 0 })
}

// waitFor waits until done reports true, failing the test after a while.
func waitFor(t *testing.T, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(time.Millisecond)
	}
}

// errDiskFull is the error the test files fail with.
var errDiskFull = errors.New("disk full")

// flakyReplace replaces the file as atomicfile.Write does, but fails the calls it
// is told to, counting from 1: those in failOn, and every one from failFrom
// if it is set.
type flakyReplace struct {
	failOn   map[int]bool
	failFrom int

	calls atomic.Int64
}

func (r *flakyReplace) replace(path string, data []byte, perm os.FileMode) error {
	n := int(r.calls.Add(1))
	if r.failOn[n] || r.failFrom > 0 && n >= r.failFrom {
		return errDiskFull
	}

	return atomicfile.Write(path, data, perm)
}

// count returns how many times replace has been called.
func (r *flakyReplace) count() int { return int(r.calls.Load()) }

// failingFile opens the events file with writes that fail: those in
// failWrites, counting from 1 across every file it opens, after writing half
// the line, or every one if failAll is set. Only the writer goroutine writes.
type failingFile struct {
	failWrites map[int]bool
	failAll    bool

	writes int
}

func (f *failingFile) open(path string) (io.WriteCloser, error) {
	file, err := openAppend(path)
	if err != nil {
		return nil, err
	}

	return &failingWriter{WriteCloser: file, f: f}, nil
}

// failingWriter is a file whose writes fail as its failingFile says.
type failingWriter struct {
	io.WriteCloser
	f *failingFile
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.f.writes++
	if w.f.failAll || w.f.failWrites[w.f.writes] {
		n, _ := w.WriteCloser.Write(p[:len(p)/2])
		return n, errDiskFull
	}

	return w.WriteCloser.Write(p)
}

// fakeClock is a time a test moves by hand, safe to read from the writer
// goroutine.
type fakeClock struct{ nanos atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.nanos.Store(t0.UnixNano())

	return c
}

func (c *fakeClock) now() time.Time { return time.Unix(0, c.nanos.Load()).UTC() }

func (c *fakeClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

// syncBuffer is a buffer safe for the log's goroutines to write at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
