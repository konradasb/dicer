// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/events"
	"github.com/dicer-sh/dicer/internal/image/reference"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// eventBatch is how many events of the history are sent in one response.
const eventBatch = 500

// eventsHandler handles GetEvents.
type eventsHandler struct {
	events *events.Log
}

// GetEvents sends the history the request picks, then, if it asks to follow,
// each new event as it is recorded, until the client goes.
func (h *eventsHandler) GetEvents(
	req *dicerdv1.GetEventsRequest, stream grpc.ServerStreamingServer[dicerdv1.GetEventsResponse],
) error {
	if req.GetLimit() < 0 {
		return status.Error(codes.InvalidArgument, "limit cannot be negative")
	}

	filter := dicer.EventFilter{
		Kind:  dicer.EventKind(req.GetKind()),
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
					return status.Error(codes.ResourceExhausted, err.Error())
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

// eventNames are the names a request's name picks: itself, and, if it is an
// image reference written short, the reference in full, as images are
// known by.
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
// history is one empty batch, so that the client knows it is caught up.
func sendHistory(stream grpc.ServerStreamingServer[dicerdv1.GetEventsResponse], list []dicer.Event) error {
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

func eventToProto(e dicer.Event) *dicerdv1.Event {
	return &dicerdv1.Event{
		Time:       timestamppb.New(e.Time),
		Kind:       string(e.Kind),
		Id:         e.ID,
		Name:       e.Name,
		Action:     string(e.Action),
		Message:    e.Message,
		Attributes: e.Attributes,
	}
}
