// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
)

// Recorder records what happens to images. It is declared here, and satisfied
// by internal/events, so that this package reports what it does without
// knowing who listens.
type Recorder interface {
	Record(e events.Event)
}

// discardRecorder is the Recorder used when none is configured.
type discardRecorder struct{}

func (discardRecorder) Record(events.Event) {}

// record records that action happened to image: known by its reference, with
// its digest among the attributes.
func (m *Manager) record(image *types.Image, action events.Action, message string, attrs map[string]string) {
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["digest"] = image.Digest

	m.events.Record(events.Event{
		Kind:       events.KindImage,
		Name:       image.Name,
		Action:     action,
		Message:    message,
		Attributes: attrs,
	})
}
