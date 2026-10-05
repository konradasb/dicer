// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

func TestDecideRestart(t *testing.T) {
	clean := Exit{}
	failed := Exit{Failure: errors.New("crashed")}
	short := time.Second

	tests := []struct {
		name     string
		policy   types.RestartPolicy
		exit     Exit
		restarts int
		ranFor   time.Duration
		want     restartDecision
	}{
		{name: "no, failure", policy: types.RestartPolicy{Mode: types.RestartModeNo}, exit: failed},
		{name: "zero policy is no", exit: failed},
		{name: "on-failure, clean", policy: types.RestartPolicy{Mode: types.RestartModeOnFailure}, exit: clean},
		{
			name: "on-failure, failure", policy: types.RestartPolicy{Mode: types.RestartModeOnFailure}, exit: failed,
			want: restartDecision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "always, clean", policy: types.RestartPolicy{Mode: types.RestartModeAlways}, exit: clean,
			want: restartDecision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "unless-stopped, failure", policy: types.RestartPolicy{Mode: types.RestartModeUnlessStopped}, exit: failed,
			want: restartDecision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "backoff doubles", policy: types.RestartPolicy{Mode: types.RestartModeAlways}, exit: failed,
			restarts: 3, ranFor: short,
			want: restartDecision{restart: true, delay: 8 * time.Second, restarts: 4},
		},
		{
			name: "backoff is capped", policy: types.RestartPolicy{Mode: types.RestartModeAlways}, exit: failed,
			restarts: 20, ranFor: short,
			want: restartDecision{restart: true, delay: restartBackoffMax, restarts: 21},
		},
		{
			name: "a long run resets the count", policy: types.RestartPolicy{Mode: types.RestartModeAlways}, exit: failed,
			restarts: 6, ranFor: restartBackoffReset,
			want: restartDecision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "retries left", policy: types.RestartPolicy{Mode: types.RestartModeOnFailure, MaxRetries: 3}, exit: failed,
			restarts: 2, ranFor: short,
			want: restartDecision{restart: true, delay: 4 * time.Second, restarts: 3},
		},
		{
			name: "retries used up", policy: types.RestartPolicy{Mode: types.RestartModeOnFailure, MaxRetries: 3}, exit: failed,
			restarts: 3, ranFor: short,
			want: restartDecision{restarts: 3, gaveUp: true},
		},
		{
			name: "a long run earns the retries back", policy: types.RestartPolicy{Mode: types.RestartModeOnFailure, MaxRetries: 3},
			exit: failed, restarts: 3, ranFor: restartBackoffReset,
			want: restartDecision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "a clean end is not a give-up", policy: types.RestartPolicy{Mode: types.RestartModeOnFailure, MaxRetries: 3},
			exit: clean, restarts: 3, ranFor: short,
			want: restartDecision{restarts: 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideRestart(tt.policy, tt.exit, tt.restarts, tt.ranFor); got != tt.want {
				t.Errorf("decideRestart = %+v, want %+v", got, tt.want)
			}
		})
	}
}
