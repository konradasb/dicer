// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

func TestRestartPolicyValidate(t *testing.T) {
	valid := []types.RestartPolicy{
		{},
		{Mode: types.RestartNo},
		{Mode: types.RestartOnFailure},
		{Mode: types.RestartOnFailure, MaxRetries: 5},
		{Mode: types.RestartUnlessStopped},
		{Mode: types.RestartAlways},
	}
	for _, p := range valid {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v.Validate() = %v", p, err)
		}
	}

	invalid := []types.RestartPolicy{
		{Mode: "sometimes"},
		{Mode: types.RestartOnFailure, MaxRetries: -1},
		{Mode: types.RestartAlways, MaxRetries: 3},
		{Mode: types.RestartNo, MaxRetries: 1},
	}
	for _, p := range invalid {
		if err := p.Validate(); !errors.Is(err, errdefs.ErrInvalidArgument) {
			t.Errorf("%+v.Validate() = %v, want an invalid argument", p, err)
		}
	}
}

func TestRestartPolicyString(t *testing.T) {
	tests := []struct {
		p    types.RestartPolicy
		want string
	}{
		{types.RestartPolicy{}, "no"},
		{types.RestartPolicy{Mode: types.RestartNo}, "no"},
		{types.RestartPolicy{Mode: types.RestartOnFailure}, "on-failure"},
		{types.RestartPolicy{Mode: types.RestartOnFailure, MaxRetries: 5}, "on-failure:5"},
		{types.RestartPolicy{Mode: types.RestartUnlessStopped}, "unless-stopped"},
		{types.RestartPolicy{Mode: types.RestartAlways}, "always"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestDecide(t *testing.T) {
	clean := Exit{}
	failed := failedExit(errors.New("crashed"))
	short := time.Second

	tests := []struct {
		name     string
		policy   types.RestartPolicy
		exit     Exit
		restarts int
		ranFor   time.Duration
		want     decision
	}{
		{name: "no, failure", policy: types.RestartPolicy{Mode: types.RestartNo}, exit: failed},
		{name: "zero policy is no", exit: failed},
		{name: "on-failure, clean", policy: types.RestartPolicy{Mode: types.RestartOnFailure}, exit: clean},
		{
			name: "on-failure, failure", policy: types.RestartPolicy{Mode: types.RestartOnFailure}, exit: failed,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "always, clean", policy: types.RestartPolicy{Mode: types.RestartAlways}, exit: clean,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "unless-stopped, failure", policy: types.RestartPolicy{Mode: types.RestartUnlessStopped}, exit: failed,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "backoff doubles", policy: types.RestartPolicy{Mode: types.RestartAlways}, exit: failed,
			restarts: 3, ranFor: short,
			want: decision{restart: true, delay: 8 * time.Second, restarts: 4},
		},
		{
			name: "backoff is capped", policy: types.RestartPolicy{Mode: types.RestartAlways}, exit: failed,
			restarts: 20, ranFor: short,
			want: decision{restart: true, delay: restartBackoffMax, restarts: 21},
		},
		{
			name: "a long run resets the count", policy: types.RestartPolicy{Mode: types.RestartAlways}, exit: failed,
			restarts: 6, ranFor: restartBackoffReset,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "retries left", policy: types.RestartPolicy{Mode: types.RestartOnFailure, MaxRetries: 3}, exit: failed,
			restarts: 2, ranFor: short,
			want: decision{restart: true, delay: 4 * time.Second, restarts: 3},
		},
		{
			name: "retries used up", policy: types.RestartPolicy{Mode: types.RestartOnFailure, MaxRetries: 3}, exit: failed,
			restarts: 3, ranFor: short,
			want: decision{restarts: 3, gaveUp: true},
		},
		{
			name: "a long run earns the retries back", policy: types.RestartPolicy{Mode: types.RestartOnFailure, MaxRetries: 3},
			exit: failed, restarts: 3, ranFor: restartBackoffReset,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "a clean end is not a give-up", policy: types.RestartPolicy{Mode: types.RestartOnFailure, MaxRetries: 3},
			exit: clean, restarts: 3, ranFor: short,
			want: decision{restarts: 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decide(tt.policy, tt.exit, tt.restarts, tt.ranFor); got != tt.want {
				t.Errorf("decide = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStartsOnBoot(t *testing.T) {
	tests := []struct {
		mode          types.RestartMode
		stoppedByUser bool
		want          bool
	}{
		{types.RestartNo, false, false},
		{types.RestartOnFailure, false, false},
		{types.RestartUnlessStopped, false, true},
		{types.RestartUnlessStopped, true, false},
		{types.RestartAlways, false, true},
		{types.RestartAlways, true, true},
	}
	for _, tt := range tests {
		if got := (types.RestartPolicy{Mode: tt.mode}).StartsOnBoot(tt.stoppedByUser); got != tt.want {
			t.Errorf("%s, stopped by user %v: startsOnBoot = %v, want %v", tt.mode, tt.stoppedByUser, got, tt.want)
		}
	}
}
