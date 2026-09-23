// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"github.com/dicer-sh/dicer"
)

// Events records what happens to images. It is declared here, and satisfied
// by internal/events, so that this package reports what it does without
// knowing who listens.
type Events interface {
	Record(e dicer.Event)
}

// discardEvents is the Events used when none is configured.
type discardEvents struct{}

func (discardEvents) Record(dicer.Event) {}

// record records that action happened to img: known by its reference, with
// its digest among the attributes.
func (m *Manager) record(img *dicer.Image, action dicer.EventAction, message string, attrs map[string]string) {
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["digest"] = img.Digest

	m.events.Record(dicer.Event{
		Kind:       dicer.KindImage,
		Name:       img.Name,
		Action:     action,
		Message:    message,
		Attributes: attrs,
	})
}
