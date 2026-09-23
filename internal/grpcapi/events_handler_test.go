// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// eventStream is a GetEvents stream that collects what is sent.
type eventStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent chan *dicerdv1.GetEventsResponse
}

func newEventStream(ctx context.Context) *eventStream {
	return &eventStream{ctx: ctx, sent: make(chan *dicerdv1.GetEventsResponse, 10)}
}

func (s *eventStream) Context() context.Context { return s.ctx }

func (s *eventStream) Send(resp *dicerdv1.GetEventsResponse) error {
	s.sent <- resp
	return nil
}

// history runs a GetEvents that does not follow and returns what it sent.
func history(t *testing.T, h *eventsHandler, req *dicerdv1.GetEventsRequest) []*dicerdv1.GetEventsResponse {
	t.Helper()

	stream := newEventStream(t.Context())
	stream.sent = make(chan *dicerdv1.GetEventsResponse, 100)
	if err := h.GetEvents(req, stream); err != nil {
		t.Fatal(err)
	}
	close(stream.sent)

	var out []*dicerdv1.GetEventsResponse
	for resp := range stream.sent {
		out = append(out, resp)
	}
	return out
}

func newEventsHandler(t *testing.T) (*eventsHandler, *events.Log) {
	t.Helper()

	log, err := events.Open(events.Config{
		Path:   filepath.Join(t.TempDir(), "events.jsonl"),
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return &eventsHandler{events: log}, log
}

func TestGetEventsHistory(t *testing.T) {
	h, log := newEventsHandler(t)
	log.Record(types.Event{Kind: types.KindInstance, ID: "1", Name: "web", Action: types.ActionCreated})
	log.Record(types.Event{Kind: types.KindImage, Name: "nginx", Action: types.ActionPulled})
	log.Record(types.Event{
		Kind: types.KindInstance, ID: "1", Name: "web", Action: types.ActionDied,
		Message: "exit code 1", Attributes: map[string]string{"exit_code": "1"},
	})

	sent := history(t, h, &dicerdv1.GetEventsRequest{Kind: dicerdv1.EventKind_EVENT_KIND_INSTANCE, Limit: 1})
	if len(sent) != 1 || !sent[0].GetCaughtUp() {
		t.Fatalf("sent %v, want one batch, caught up", sent)
	}
	got := sent[0].GetEvents()
	if len(got) != 1 || got[0].GetAction() != dicerdv1.EventAction_EVENT_ACTION_DIED || got[0].GetAttributes()["exit_code"] != "1" ||
		got[0].GetMessage() == "" || got[0].GetTime() == nil {
		t.Errorf("sent %v, want the last instance event, whole", got)
	}
}

// A long history comes in batches, the last one marked; an empty one is an
// empty batch, marked, so that a client knows it has it all.
func TestGetEventsBatchesTheHistory(t *testing.T) {
	h, log := newEventsHandler(t)

	if sent := history(t, h, &dicerdv1.GetEventsRequest{}); len(sent) != 1 ||
		len(sent[0].GetEvents()) != 0 || !sent[0].GetCaughtUp() {
		t.Errorf("empty history sent %v, want one empty batch, caught up", sent)
	}

	for range eventBatch + 1 {
		log.Record(types.Event{Kind: types.KindInstance, Name: "web", Action: types.ActionStarted})
	}
	sent := history(t, h, &dicerdv1.GetEventsRequest{})
	if len(sent) != 2 || len(sent[0].GetEvents()) != eventBatch || sent[0].GetCaughtUp() ||
		len(sent[1].GetEvents()) != 1 || !sent[1].GetCaughtUp() {
		t.Errorf("sent %d batches, want a full one, then the last one caught up", len(sent))
	}
}

// An image is known by its full reference, and found by the short one too.
func TestGetEventsFindsAnImageByItsShortName(t *testing.T) {
	h, log := newEventsHandler(t)
	log.Record(types.Event{Kind: types.KindImage, Name: "docker.io/library/busybox:latest", Action: types.ActionPulled})
	log.Record(types.Event{Kind: types.KindInstance, Name: "web", Action: types.ActionStarted})

	for _, name := range []string{"busybox", "busybox:latest", "docker.io/library/busybox:latest"} {
		sent := history(t, h, &dicerdv1.GetEventsRequest{Name: name})
		if got := sent[0].GetEvents(); len(got) != 1 || got[0].GetAction() != dicerdv1.EventAction_EVENT_ACTION_PULLED {
			t.Errorf("--name %s found %v, want the image's event", name, got)
		}
	}
	if got := history(t, h, &dicerdv1.GetEventsRequest{Name: "web"})[0].GetEvents(); len(got) != 1 {
		t.Errorf("--name web found %v, want the instance's event", got)
	}
}

// Following sends the history, then what happens next, until the client
// goes.
func TestGetEventsFollow(t *testing.T) {
	h, log := newEventsHandler(t)
	log.Record(types.Event{Kind: types.KindInstance, Name: "web", Action: types.ActionStarted})

	ctx, cancel := context.WithCancel(t.Context())
	stream := newEventStream(ctx)
	done := make(chan error, 1)
	go func() { done <- h.GetEvents(&dicerdv1.GetEventsRequest{Follow: true}, stream) }()

	next := func() *dicerdv1.GetEventsResponse {
		select {
		case resp := <-stream.sent:
			return resp
		case <-time.After(5 * time.Second):
			t.Fatal("nothing was sent")
			return nil
		}
	}
	if resp := next(); !resp.GetCaughtUp() || len(resp.GetEvents()) != 1 || resp.GetEvents()[0].GetAction() != dicerdv1.EventAction_EVENT_ACTION_STARTED {
		t.Errorf("first = %v, want the history, caught up", resp)
	}
	log.Record(types.Event{Kind: types.KindInstance, Name: "web", Action: types.ActionStopped})
	if resp := next(); len(resp.GetEvents()) != 1 || resp.GetEvents()[0].GetAction() != dicerdv1.EventAction_EVENT_ACTION_STOPPED {
		t.Errorf("then = %v, want the new event", resp)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("GetEvents = %v, want a clean end when the client goes", err)
	}
}

func TestGetEventsRejectsANegativeLimit(t *testing.T) {
	h, _ := newEventsHandler(t)

	if err := h.GetEvents(&dicerdv1.GetEventsRequest{Limit: -1}, newEventStream(t.Context())); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("GetEvents = %v, want InvalidArgument", err)
	}
}
