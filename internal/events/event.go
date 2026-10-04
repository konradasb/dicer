// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package events

import (
	"slices"
	"time"
)

// Kind is the kind of resource an event is about.
type Kind string

// The kinds of resource events are recorded for.
const (
	KindInstance Kind = "instance"
	KindImage    Kind = "image"
	KindNetwork  Kind = "network"
	KindVolume   Kind = "volume"
	KindKernel   Kind = "kernel"
)

// Action is what happened to the resource.
type Action string

// What happens to any kind of resource.
const (
	ActionCreated Action = "created"
	ActionUpdated Action = "updated"
	ActionDeleted Action = "deleted"
)

// What happens to an instance.
const (
	ActionStarted Action = "started"
	ActionStopped Action = "stopped"
	ActionPaused  Action = "paused"
	ActionResumed Action = "resumed"

	// ActionExited is an instance whose guest ended cleanly, of its own
	// accord; ActionDied one whose guest ended in failure.
	ActionExited Action = "exited"
	ActionDied   Action = "died"

	// ActionRestarting is an instance its restart policy will start again.
	ActionRestarting Action = "restarting"

	// ActionRenamed is an instance given another name.
	ActionRenamed Action = "renamed"

	// ActionHealthy and ActionUnhealthy are an instance's health check
	// reaching a verdict.
	ActionHealthy   Action = "healthy"
	ActionUnhealthy Action = "unhealthy"

	ActionSnapshotCreated  Action = "snapshot_created"
	ActionSnapshotRestored Action = "snapshot_restored"
	ActionSnapshotDeleted  Action = "snapshot_deleted"
)

// What happens to an image.
const (
	ActionPulled Action = "pulled"

	// ActionCollected is an image garbage collection removed.
	ActionCollected Action = "collected"
)

// What happens to a kernel.
const (
	// ActionImported is a kernel recorded by its URL, to be fetched when an
	// instance first starts with it.
	ActionImported Action = "imported"

	// ActionFetched is a kernel downloaded, or copied from a local path, and
	// verified.
	ActionFetched Action = "fetched"
)

// Event is one thing that happened to one resource.
type Event struct {
	Time time.Time `json:"time"`
	Kind Kind      `json:"kind"`

	// ID and Name are the resource's. The ID tells apart two resources that
	// had the same name at different times; a resource with no ID of its
	// own, such as an image, is known by its name.
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`

	Action Action `json:"action"`

	// Message says what happened in a line, for a person: why an instance
	// died, where an image came from.
	Message string `json:"message,omitempty"`

	// Attributes say it for a program: exit_code, restart_count, digest.
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Filter picks events. The zero Filter picks every event.
type Filter struct {
	Kind Kind
	ID   string

	// Names picks the events about a resource with any of these names: an
	// image's is one, however it was written.
	Names []string

	// Since picks the events recorded at or after it.
	Since time.Time
}

// Matches reports whether the filter picks e.
func (f Filter) Matches(e Event) bool {
	return (f.Kind == "" || e.Kind == f.Kind) &&
		(f.ID == "" || e.ID == f.ID) &&
		(len(f.Names) == 0 || slices.Contains(f.Names, e.Name)) &&
		!e.Time.Before(f.Since)
}
