// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package events records what happens to the resources on this host -- an
// instance started, crashed, was restarted; an image was pulled or collected
// -- for people and for anything built on top of Dicer.
//
// What an event is, and the kinds and actions one may report, is dicer.Event.
// This package is the log: where events are kept, who is subscribed, and what
// each subscriber has seen. Reporting on a new kind of resource is a matter
// of adding its constants to the root package and having whatever manages it
// record them; the log, the API and the CLI need no change.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"sync"
	"time"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/atomicfile"
)

// DefaultMaxCount is how many events a Log keeps unless told otherwise.
const DefaultMaxCount = 10000

// subscriberBuffer is how many events a subscriber may fall behind by before
// it is cut off.
const subscriberBuffer = 256

// ErrFellBehind ends a subscription that did not keep up with the events.
var ErrFellBehind = errors.New("events came faster than they were read; subscribe again to catch up")

// Config configures a Log.
type Config struct {
	// Path is the file the events are kept in, one JSON document a line.
	Path string

	// MaxCount is how many events are kept: the most recent. Defaults to
	// DefaultMaxCount.
	MaxCount int

	// MaxAge is how long an event is kept. Zero means no limit.
	MaxAge time.Duration

	// Logger is where the Log logs. Optional: defaults to slog.Default.
	Logger *slog.Logger
}

// Log keeps the events recorded on this host, and passes each new one on to
// whoever is subscribed.
//
// The events are kept in a file, appended to as they are recorded, so that
// they outlive the daemon, and in memory, so that reading them costs no
// disk. The file is rewritten with only what is kept when the log is opened
// and when it has grown past its limit by a quarter.
type Log struct {
	path     string
	maxCount int
	maxAge   time.Duration
	logger   *slog.Logger

	// now is the clock events are stamped and aged by. It is a field so
	// that tests need not wait.
	now func() time.Time

	mu      sync.Mutex
	file    *os.File
	events  []dicer.Event // oldest first
	written int           // events in the file, kept or not
	subs    map[*Subscription]struct{}
}

// Open opens the log at cfg.Path, creating it if it does not exist.
func Open(cfg Config) (*Log, error) {
	if cfg.MaxCount <= 0 {
		cfg.MaxCount = DefaultMaxCount
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	l := &Log{
		path:     cfg.Path,
		maxCount: cfg.MaxCount,
		maxAge:   cfg.MaxAge,
		logger:   cfg.Logger.With("component", "events"),
		now:      time.Now,
		subs:     make(map[*Subscription]struct{}),
	}

	if err := l.load(); err != nil {
		return nil, err
	}
	if err := l.compact(); err != nil {
		return nil, err
	}
	return l, nil
}

// load reads the events kept in the file. A line that cannot be read -- the
// tail of a write the daemon died during -- is skipped.
func (l *Log) load() error {
	data, err := os.ReadFile(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read events: %w", err)
	}

	for line := range bytes.Lines(data) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e dicer.Event
		if err := json.Unmarshal(line, &e); err != nil {
			l.logger.Warn("skipping an unreadable event", "error", err)
			continue
		}
		l.events = append(l.events, e)
	}
	return nil
}

// Record stamps e with the time, if it has none, keeps it, and passes it on
// to every subscriber whose filter picks it. A failure to write it to disk
// is logged rather than returned: whatever happened has happened, and the
// operation that reports it must not fail for it.
func (l *Log) Record(e dicer.Event) {
	if e.Time.IsZero() {
		e.Time = l.now()
	}
	e = clone(e)

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.append(e); err != nil {
		l.logger.Warn("cannot write an event", "kind", e.Kind, "name", e.Name, "action", e.Action, "error", err)
	}
	l.events = append(l.events, e)
	l.trim()

	if l.written > l.maxCount+l.maxCount/4 {
		if err := l.compact(); err != nil {
			l.logger.Warn("cannot compact the events", "error", err)
		}
	}

	for sub := range l.subs {
		if sub.filter.Matches(e) {
			sub.send(clone(e))
		}
	}
}

// append writes e to the end of the file. The caller must hold l.mu.
func (l *Log) append(e dicer.Event) error {
	if l.file == nil {
		return errors.New("the events file is not open")
	}

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := l.file.Write(append(line, '\n')); err != nil {
		return err
	}
	l.written++
	return nil
}

// trim drops the events past the log's limits, oldest first. The caller
// must hold l.mu.
func (l *Log) trim() {
	drop := max(len(l.events)-l.maxCount, 0)
	if l.maxAge > 0 {
		oldest := l.now().Add(-l.maxAge)
		for drop < len(l.events) && l.events[drop].Time.Before(oldest) {
			drop++
		}
	}
	if drop > 0 {
		l.events = append(l.events[:0:0], l.events[drop:]...)
	}
}

// compact rewrites the file with only the events kept, and reopens it for
// appending. The caller must hold l.mu, or be Open.
func (l *Log) compact() error {
	l.trim()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range l.events {
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("encode event: %w", err)
		}
	}
	if err := atomicfile.Write(l.path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write events: %w", err)
	}

	if l.file != nil {
		_ = l.file.Close()
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.file = nil
		return fmt.Errorf("open events: %w", err)
	}
	l.file = f
	l.written = len(l.events)
	return nil
}

// List returns the events f picks, oldest first: the last limit of them, or
// all if limit is zero.
func (l *Log) List(f dicer.EventFilter, limit int) []dicer.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list(f, limit)
}

// list is List. The caller must hold l.mu.
func (l *Log) list(f dicer.EventFilter, limit int) []dicer.Event {
	var out []dicer.Event
	for _, e := range l.events {
		if f.Matches(e) {
			out = append(out, clone(e))
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Subscribe returns the events f picks so far, as List does, and a
// subscription to those recorded from then on. The two are taken together,
// so that none is missed between them and none is in both. The caller must
// Close the subscription.
func (l *Log) Subscribe(f dicer.EventFilter, limit int) ([]dicer.Event, *Subscription) {
	sub := &Subscription{
		filter: f,
		events: make(chan dicer.Event, subscriberBuffer),
		done:   make(chan struct{}),
		log:    l,
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.subs[sub] = struct{}{}
	return l.list(f, limit), sub
}

// Close stops the log: its subscriptions end, and nothing more is written.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for sub := range l.subs {
		sub.end(nil)
	}
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Subscription receives the events recorded after it was made.
type Subscription struct {
	filter dicer.EventFilter
	events chan dicer.Event
	log    *Log

	// done is closed when the subscription ends; err says why, nil for a
	// subscription closed by its owner or its log.
	done chan struct{}
	err  error
}

// Events returns the events as they are recorded. It is closed when the
// subscription ends; Err then says why.
func (s *Subscription) Events() <-chan dicer.Event { return s.events }

// Err returns why the subscription ended, once Events is closed:
// ErrFellBehind, or nil.
func (s *Subscription) Err() error {
	<-s.done
	return s.err
}

// Close ends the subscription.
func (s *Subscription) Close() {
	s.log.mu.Lock()
	defer s.log.mu.Unlock()
	s.end(nil)
}

// send passes e on, or ends a subscription whose reader has fallen too far
// behind: the log must not wait on a slow reader. The caller must hold the
// log's lock.
func (s *Subscription) send(e dicer.Event) {
	select {
	case s.events <- e:
	default:
		s.end(ErrFellBehind)
	}
}

// end ends the subscription for err, once. The caller must hold the log's
// lock.
func (s *Subscription) end(err error) {
	if _, ok := s.log.subs[s]; !ok {
		return
	}
	delete(s.log.subs, s)
	s.err = err
	close(s.events)
	close(s.done)
}

// clone returns e with a copy of its attributes, so that what one holder of
// an event does to them does not reach another's.
func clone(e dicer.Event) dicer.Event {
	e.Attributes = maps.Clone(e.Attributes)

	return e
}
