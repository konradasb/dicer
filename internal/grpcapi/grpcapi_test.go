// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/events"
)

// wantCode checks that an error reached the client in the class the handler
// put it in, which is the whole of what a client can match on.
func wantClass(t *testing.T, err, class error) {
	t.Helper()

	if !errors.Is(err, class) {
		t.Errorf("error %v is not in class %v", err, class)
	}
}

// fakeRecorder keeps the events recorded.
type fakeRecorder struct {
	events []events.Event
}

func (f *fakeRecorder) Record(e events.Event) { f.events = append(f.events, e) }
