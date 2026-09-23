// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// eventBatch is how many events of the history are sent in one response.
const eventBatch = 500

// eventsHandler handles GetEvents.
type eventsHandler struct {
	events *events.Log
}

// GetEvents sends the matching history, then new events if the request
// follows.
func (h *eventsHandler) GetEvents(
	req *dicerdv1.GetEventsRequest, stream grpc.ServerStreamingServer[dicerdv1.GetEventsResponse],
) error {
	if req.GetLimit() < 0 {
		return errdefs.InvalidArgument("limit cannot be negative")
	}

	kind, err := eventKinds.fromProto(req.GetKind())
	if err != nil {
		return err
	}
	filter := types.EventFilter{
		Kind:  kind,
		ID:    req.GetId(),
		Names: eventNames(req.GetName()),
	}
	if since := req.GetSince(); since != nil {
		filter.Since = since.AsTime()
	}
	limit := int(req.GetLimit())

	if !req.GetFollow() {
		return sendHistory(stream, h.events.List(filter, limit))
	}

	history, sub := h.events.Subscribe(filter, limit)
	defer sub.Close()

	if err := sendHistory(stream, history); err != nil {
		return err
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case e, ok := <-sub.Events():
			if !ok {
				if err := sub.Err(); errors.Is(err, events.ErrFellBehind) {
					return errdefs.ResourceExhausted("%v", err)
				}
				return nil
			}
			resp := &dicerdv1.GetEventsResponse{Events: []*dicerdv1.Event{eventToProto(e)}}
			if err := stream.Send(resp); err != nil {
				return err
			}
		}
	}
}

// eventNames returns the names a request's name matches: itself and, for a
// short image reference, its full form.
func eventNames(name string) []string {
	if name == "" {
		return nil
	}
	names := []string{name}
	if ref, err := reference.Parse(name); err == nil && ref.String() != name {
		names = append(names, ref.String())
	}
	return names
}

// sendHistory sends list in batches, the last marked caught up. An empty
// history is sent as one empty batch.
func sendHistory(stream grpc.ServerStreamingServer[dicerdv1.GetEventsResponse], list []types.Event) error {
	for {
		n := min(len(list), eventBatch)
		resp := &dicerdv1.GetEventsResponse{
			Events:   make([]*dicerdv1.Event, 0, n),
			CaughtUp: n == len(list),
		}
		for _, e := range list[:n] {
			resp.Events = append(resp.Events, eventToProto(e))
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
		if resp.GetCaughtUp() {
			return nil
		}
		list = list[n:]
	}
}

func eventToProto(e types.Event) *dicerdv1.Event {
	return &dicerdv1.Event{
		Time:       timestamppb.New(e.Time),
		Kind:       eventKinds.toProto(e.Kind),
		Id:         e.ID,
		Name:       e.Name,
		Action:     eventActions.toProto(e.Action),
		Message:    e.Message,
		Attributes: e.Attributes,
	}
}
