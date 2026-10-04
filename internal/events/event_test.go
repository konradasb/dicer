// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package events

import (
	"testing"
	"time"
)

// TestFilterSelectsMatchingEvents checks a filter selects an event only when
// every field it sets matches.
func TestFilterSelectsMatchingEvents(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	instance := Event{Time: now, Kind: KindInstance, ID: "id-web", Name: "web", Action: ActionStarted}

	tests := []struct {
		name   string
		filter Filter
		want   bool
	}{
		{"zero filter selects every event", Filter{}, true},
		{"same kind", Filter{Kind: KindInstance}, true},
		{"other kind", Filter{Kind: KindImage}, false},
		{"same id", Filter{ID: "id-web"}, true},
		{"other id", Filter{ID: "id-db"}, false},
		{"one of the names", Filter{Names: []string{"db", "web"}}, true},
		{"none of the names", Filter{Names: []string{"db"}}, false},
		{"recorded at since", Filter{Since: now}, true},
		{"recorded after since", Filter{Since: now.Add(-time.Second)}, true},
		{"recorded before since", Filter{Since: now.Add(time.Second)}, false},
		{
			"every field matches",
			Filter{Kind: KindInstance, ID: "id-web", Names: []string{"web"}, Since: now},
			true,
		},
		{
			"one field of several does not match",
			Filter{Kind: KindInstance, ID: "id-db", Names: []string{"web"}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.Matches(instance); got != tt.want {
				t.Errorf("Matches() = %v, want %v", got, tt.want)
			}
		})
	}
}
