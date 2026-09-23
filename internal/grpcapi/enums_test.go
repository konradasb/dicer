// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// TestEnumsRoundTrip checks every value of the daemon's enumerations has a
// value in the API's, and comes back as itself.
func TestEnumsRoundTrip(t *testing.T) {
	roundTrips(t, hypervisorTypes)
	roundTrips(t, instanceStates)
	roundTrips(t, initModes)
	roundTrips(t, restartModes)
	roundTrips(t, mountTypes)
	roundTrips(t, protocols)
	roundTrips(t, healthStatuses)
	roundTrips(t, architectures)
	roundTrips(t, eventKinds)
	roundTrips(t, eventActions)
	roundTrips(t, logSources)
	roundTrips(t, pullStages)
}

func roundTrips[T comparable, P ~int32](t *testing.T, e enum[T, P]) {
	t.Helper()

	seen := make(map[P]T, len(e.values))
	for v, p := range e.values {
		if p == 0 {
			t.Errorf("%s %v has no API value", e.what, v)
		}
		if other, dup := seen[p]; dup {
			t.Errorf("%s %v and %v share the API value %v", e.what, v, other, p)
		}
		seen[p] = v

		if got, err := e.fromProto(e.toProto(v)); err != nil || got != v {
			t.Errorf("%s %v came back as %v, %v", e.what, v, got, err)
		}
	}
}

// TestEnumFromProto checks unspecified is the zero value and a value the
// daemon does not know is refused.
func TestEnumFromProto(t *testing.T) {
	if got, err := initModes.fromProto(dicerdv1.InitMode_INIT_MODE_UNSPECIFIED); err != nil || got != "" {
		t.Errorf("fromProto(unspecified) = %q, %v; want the zero value", got, err)
	}
	if _, err := initModes.fromProto(dicerdv1.InitMode(99)); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("fromProto(99) = %v, want InvalidArgument", err)
	}
	if got := initModes.toProto(types.InitMode("openrc")); got != dicerdv1.InitMode_INIT_MODE_UNSPECIFIED {
		t.Errorf("toProto(openrc) = %v, want unspecified", got)
	}
}
