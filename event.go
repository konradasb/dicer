// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"time"

	"google.golang.org/grpc"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// Event is one thing that happened to one resource on the host.
type Event struct {
	Time time.Time `json:"time,omitzero"`
	Kind EventKind `json:"kind,omitzero"`

	// ID is the resource's ID, which tells apart resources that had the
	// same name at different times. It is empty for a resource known only
	// by its name, such as an image.
	ID   string `json:"id,omitzero"`
	Name string `json:"name,omitzero"`

	Action EventAction `json:"action,omitzero"`

	// Message says what happened, in a line, for a person.
	Message string `json:"message,omitzero"`

	// Attributes say what happened, for a program: exit_code,
	// restart_count, digest.
	Attributes map[string]string `json:"attributes,omitzero"`
}

// EventKind is the kind of resource an event is about.
type EventKind string

// The event kinds.
const (
	EventKindInstance EventKind = "instance"
	EventKindSnapshot EventKind = "snapshot"
	EventKindImage    EventKind = "image"
	EventKindNetwork  EventKind = "network"
	EventKindVolume   EventKind = "volume"
	EventKindKernel   EventKind = "kernel"
)

var eventKinds = enum[EventKind, dicerdv1.EventKind]{"event kind", map[EventKind]dicerdv1.EventKind{
	EventKindInstance: dicerdv1.EventKind_EVENT_KIND_INSTANCE,
	EventKindSnapshot: dicerdv1.EventKind_EVENT_KIND_SNAPSHOT,
	EventKindImage:    dicerdv1.EventKind_EVENT_KIND_IMAGE,
	EventKindNetwork:  dicerdv1.EventKind_EVENT_KIND_NETWORK,
	EventKindVolume:   dicerdv1.EventKind_EVENT_KIND_VOLUME,
	EventKindKernel:   dicerdv1.EventKind_EVENT_KIND_KERNEL,
}}

// EventAction is what happened to the resource.
type EventAction string

// The event actions.
const (
	// Any kind of resource.
	EventActionCreated EventAction = "created"
	EventActionUpdated EventAction = "updated"
	EventActionDeleted EventAction = "deleted"

	// An instance.
	EventActionStarted EventAction = "started"
	EventActionStopped EventAction = "stopped"
	EventActionPaused  EventAction = "paused"
	EventActionResumed EventAction = "resumed"

	// EventActionExited means the guest ended cleanly, of its own accord.
	EventActionExited EventAction = "exited"

	// EventActionDied means the guest ended in failure.
	EventActionDied EventAction = "died"

	// EventActionRestarting means the restart policy will start the
	// instance again.
	EventActionRestarting EventAction = "restarting"

	EventActionRenamed EventAction = "renamed"

	// EventActionHealthy and EventActionUnhealthy mean the health check
	// reached a verdict.
	EventActionHealthy   EventAction = "healthy"
	EventActionUnhealthy EventAction = "unhealthy"

	// EventActionSnapshotRestored means the instance was put back as a
	// snapshot of it holds it.
	EventActionSnapshotRestored EventAction = "snapshot_restored"

	// EventActionResized means a running instance was given other vCPUs or
	// memory.
	EventActionResized EventAction = "resized"

	// EventActionStandby means the instance was frozen to disk, and its
	// hypervisor ended.
	EventActionStandby EventAction = "standby"

	// An image.
	EventActionPulled EventAction = "pulled"

	// EventActionCollected means garbage collection removed the image.
	EventActionCollected EventAction = "collected"

	// EventActionImported means a kernel was imported, and is on the host.
	EventActionImported EventAction = "imported"
)

var eventActions = enum[EventAction, dicerdv1.EventAction]{"event action", map[EventAction]dicerdv1.EventAction{
	EventActionCreated:          dicerdv1.EventAction_EVENT_ACTION_CREATED,
	EventActionUpdated:          dicerdv1.EventAction_EVENT_ACTION_UPDATED,
	EventActionDeleted:          dicerdv1.EventAction_EVENT_ACTION_DELETED,
	EventActionStarted:          dicerdv1.EventAction_EVENT_ACTION_STARTED,
	EventActionStopped:          dicerdv1.EventAction_EVENT_ACTION_STOPPED,
	EventActionPaused:           dicerdv1.EventAction_EVENT_ACTION_PAUSED,
	EventActionResumed:          dicerdv1.EventAction_EVENT_ACTION_RESUMED,
	EventActionExited:           dicerdv1.EventAction_EVENT_ACTION_EXITED,
	EventActionDied:             dicerdv1.EventAction_EVENT_ACTION_DIED,
	EventActionRestarting:       dicerdv1.EventAction_EVENT_ACTION_RESTARTING,
	EventActionRenamed:          dicerdv1.EventAction_EVENT_ACTION_RENAMED,
	EventActionHealthy:          dicerdv1.EventAction_EVENT_ACTION_HEALTHY,
	EventActionUnhealthy:        dicerdv1.EventAction_EVENT_ACTION_UNHEALTHY,
	EventActionSnapshotRestored: dicerdv1.EventAction_EVENT_ACTION_SNAPSHOT_RESTORED,
	EventActionResized:          dicerdv1.EventAction_EVENT_ACTION_RESIZED,
	EventActionStandby:          dicerdv1.EventAction_EVENT_ACTION_STANDBY,
	EventActionPulled:           dicerdv1.EventAction_EVENT_ACTION_PULLED,
	EventActionCollected:        dicerdv1.EventAction_EVENT_ACTION_COLLECTED,
	EventActionImported:         dicerdv1.EventAction_EVENT_ACTION_IMPORTED,
}}

// EventOptions pick the events Client.Events streams: every one, unless
// narrowed.
type EventOptions struct {
	// Kind picks one kind of resource. Empty picks every kind.
	Kind EventKind

	// ID and Name pick one resource. An image's name may be given as it is
	// pulled: busybox:latest picks the events about
	// docker.io/library/busybox:latest.
	ID   string
	Name string

	// Since picks only the events at or after it.
	Since time.Time

	// Limit picks only the last this many events of the history. Zero
	// means all of it.
	Limit int

	// Follow keeps streaming new events after the history.
	Follow bool
}

// EventBatch is a batch of events, oldest first.
type EventBatch struct {
	Events []Event `json:"events,omitzero"`

	// CaughtUp is set on the last batch of the history, which is sent even
	// if the history is empty: what follows, if the stream follows, is new.
	CaughtUp bool `json:"caught_up,omitzero"`
}

// EventStream is a stream of events, read with Next.
type EventStream struct {
	stream grpc.ServerStreamingClient[dicerdv1.GetEventsResponse]
	cancel context.CancelFunc
}

// Events streams what has happened to the resources on the host: the
// history the daemon keeps, oldest first, then, if opts.Follow is set, each
// new event as it happens, none missed between the two. A follower that does
// not keep up is disconnected with ErrResourceExhausted rather than slowing
// the host. The stream must be closed when done.
func (c *Client) Events(ctx context.Context, opts EventOptions) (*EventStream, error) {
	return newEventStream(ctx, c.api, opts)
}

// newEventStream opens the stream of the events opts picks.
func newEventStream(ctx context.Context, api dicerdv1.DaemonServiceClient, opts EventOptions) (*EventStream, error) {
	req, err := getEventsRequest(opts)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	stream, err := api.GetEvents(ctx, req)
	if err != nil {
		cancel()
		return nil, fromStatus(err)
	}

	return &EventStream{stream: stream, cancel: cancel}, nil
}

// Next returns the next batch of events. It returns io.EOF once a stream
// that does not follow has sent the history, and the error the stream
// failed with otherwise, such as context.Canceled once it is closed.
func (s *EventStream) Next() (EventBatch, error) {
	resp, err := s.stream.Recv()
	if err != nil {
		return EventBatch{}, fromStatus(err)
	}
	return eventBatchFromProto(resp), nil
}

// Close ends the stream.
func (s *EventStream) Close() error {
	s.cancel()
	return nil
}

// getEventsRequest returns the request for the events opts picks.
func getEventsRequest(opts EventOptions) (*dicerdv1.GetEventsRequest, error) {
	kind, err := eventKinds.toProto(opts.Kind)
	if err != nil {
		return nil, err
	}

	return &dicerdv1.GetEventsRequest{
		Kind:   kind,
		Id:     opts.ID,
		Name:   opts.Name,
		Since:  timeToProto(opts.Since),
		Limit:  int32(opts.Limit),
		Follow: opts.Follow,
	}, nil
}

// eventBatchFromProto returns the batch p carries.
func eventBatchFromProto(p *dicerdv1.GetEventsResponse) EventBatch {
	return EventBatch{Events: convertAll(p.GetEvents(), eventFromProto), CaughtUp: p.GetCaughtUp()}
}

// eventFromProto returns the event p describes.
func eventFromProto(p *dicerdv1.Event) Event {
	return Event{
		Time:       timeFromProto(p.GetTime()),
		Kind:       eventKinds.fromProto(p.GetKind()),
		ID:         p.GetId(),
		Name:       p.GetName(),
		Action:     eventActions.fromProto(p.GetAction()),
		Message:    p.GetMessage(),
		Attributes: p.GetAttributes(),
	}
}
