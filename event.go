// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"slices"
	"time"
)

// EventKind is the kind of resource an event is about.
type EventKind string

// The kinds of resource events are recorded for.
const (
	KindInstance EventKind = "instance"
	KindImage    EventKind = "image"
)

// EventAction is what happened to the resource.
type EventAction string

// What happens to any kind of resource.
const (
	ActionCreated EventAction = "created"
	ActionUpdated EventAction = "updated"
	ActionDeleted EventAction = "deleted"
)

// What happens to an instance.
const (
	ActionStarted EventAction = "started"
	ActionStopped EventAction = "stopped"
	ActionPaused  EventAction = "paused"
	ActionResumed EventAction = "resumed"

	// ActionExited is an instance whose guest ended cleanly, of its own
	// accord; ActionDied one whose guest ended in failure.
	ActionExited EventAction = "exited"
	ActionDied   EventAction = "died"

	// ActionRestarting is an instance its restart policy will start again.
	ActionRestarting EventAction = "restarting"

	// ActionRenamed is an instance given another name.
	ActionRenamed EventAction = "renamed"

	// ActionHealthy and ActionUnhealthy are an instance's health check
	// reaching a verdict.
	ActionHealthy   EventAction = "healthy"
	ActionUnhealthy EventAction = "unhealthy"

	ActionSnapshotCreated  EventAction = "snapshot_created"
	ActionSnapshotRestored EventAction = "snapshot_restored"
	ActionSnapshotDeleted  EventAction = "snapshot_deleted"
)

// What happens to an image.
const (
	ActionPulled EventAction = "pulled"

	// ActionCollected is an image garbage collection removed.
	ActionCollected EventAction = "collected"
)

// Event is one thing that happened to one resource.
type Event struct {
	Time time.Time `json:"time"`
	Kind EventKind `json:"kind"`

	// ID and Name are the resource's. The ID tells apart two resources that
	// had the same name at different times; a resource with no ID of its
	// own, such as an image, is known by its name.
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`

	Action EventAction `json:"action"`

	// Message says what happened in a line, for a person: why an instance
	// died, where an image came from.
	Message string `json:"message,omitempty"`

	// Attributes say it for a program: exit_code, restart_count, digest.
	Attributes map[string]string `json:"attributes,omitempty"`
}

// EventFilter picks events. The zero EventFilter picks every event.
type EventFilter struct {
	Kind EventKind
	ID   string

	// Names picks the events about a resource with any of these names: an
	// image's is one, however it was written.
	Names []string

	// Since picks the events recorded at or after it.
	Since time.Time
}

// Matches reports whether the filter picks e.
func (f EventFilter) Matches(e Event) bool {
	return (f.Kind == "" || e.Kind == f.Kind) &&
		(f.ID == "" || e.ID == f.ID) &&
		(len(f.Names) == 0 || slices.Contains(f.Names, e.Name)) &&
		!e.Time.Before(f.Since)
}
