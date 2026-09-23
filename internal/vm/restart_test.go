// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
)

func TestRestartPolicyValidate(t *testing.T) {
	valid := []dicer.RestartPolicy{
		{},
		{Mode: dicer.RestartNo},
		{Mode: dicer.RestartOnFailure},
		{Mode: dicer.RestartOnFailure, MaxRetries: 5},
		{Mode: dicer.RestartUnlessStopped},
		{Mode: dicer.RestartAlways},
	}
	for _, p := range valid {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v.Validate() = %v", p, err)
		}
	}

	invalid := []dicer.RestartPolicy{
		{Mode: "sometimes"},
		{Mode: dicer.RestartOnFailure, MaxRetries: -1},
		{Mode: dicer.RestartAlways, MaxRetries: 3},
		{Mode: dicer.RestartNo, MaxRetries: 1},
	}
	for _, p := range invalid {
		if err := p.Validate(); !errors.Is(err, dicer.ErrInvalidArgument) {
			t.Errorf("%+v.Validate() = %v, want an invalid argument", p, err)
		}
	}
}

func TestRestartPolicyString(t *testing.T) {
	tests := []struct {
		p    dicer.RestartPolicy
		want string
	}{
		{dicer.RestartPolicy{}, "no"},
		{dicer.RestartPolicy{Mode: dicer.RestartNo}, "no"},
		{dicer.RestartPolicy{Mode: dicer.RestartOnFailure}, "on-failure"},
		{dicer.RestartPolicy{Mode: dicer.RestartOnFailure, MaxRetries: 5}, "on-failure:5"},
		{dicer.RestartPolicy{Mode: dicer.RestartUnlessStopped}, "unless-stopped"},
		{dicer.RestartPolicy{Mode: dicer.RestartAlways}, "always"},
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
		policy   dicer.RestartPolicy
		exit     Exit
		restarts int
		ranFor   time.Duration
		want     decision
	}{
		{name: "no, failure", policy: dicer.RestartPolicy{Mode: dicer.RestartNo}, exit: failed},
		{name: "zero policy is no", exit: failed},
		{name: "on-failure, clean", policy: dicer.RestartPolicy{Mode: dicer.RestartOnFailure}, exit: clean},
		{
			name: "on-failure, failure", policy: dicer.RestartPolicy{Mode: dicer.RestartOnFailure}, exit: failed,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "always, clean", policy: dicer.RestartPolicy{Mode: dicer.RestartAlways}, exit: clean,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "unless-stopped, failure", policy: dicer.RestartPolicy{Mode: dicer.RestartUnlessStopped}, exit: failed,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "backoff doubles", policy: dicer.RestartPolicy{Mode: dicer.RestartAlways}, exit: failed,
			restarts: 3, ranFor: short,
			want: decision{restart: true, delay: 8 * time.Second, restarts: 4},
		},
		{
			name: "backoff is capped", policy: dicer.RestartPolicy{Mode: dicer.RestartAlways}, exit: failed,
			restarts: 20, ranFor: short,
			want: decision{restart: true, delay: restartBackoffMax, restarts: 21},
		},
		{
			name: "a long run resets the count", policy: dicer.RestartPolicy{Mode: dicer.RestartAlways}, exit: failed,
			restarts: 6, ranFor: restartBackoffReset,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "retries left", policy: dicer.RestartPolicy{Mode: dicer.RestartOnFailure, MaxRetries: 3}, exit: failed,
			restarts: 2, ranFor: short,
			want: decision{restart: true, delay: 4 * time.Second, restarts: 3},
		},
		{
			name: "retries used up", policy: dicer.RestartPolicy{Mode: dicer.RestartOnFailure, MaxRetries: 3}, exit: failed,
			restarts: 3, ranFor: short,
			want: decision{restarts: 3, gaveUp: true},
		},
		{
			name: "a long run earns the retries back", policy: dicer.RestartPolicy{Mode: dicer.RestartOnFailure, MaxRetries: 3},
			exit: failed, restarts: 3, ranFor: restartBackoffReset,
			want: decision{restart: true, delay: time.Second, restarts: 1},
		},
		{
			name: "a clean end is not a give-up", policy: dicer.RestartPolicy{Mode: dicer.RestartOnFailure, MaxRetries: 3},
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
		mode          dicer.RestartMode
		stoppedByUser bool
		want          bool
	}{
		{dicer.RestartNo, false, false},
		{dicer.RestartOnFailure, false, false},
		{dicer.RestartUnlessStopped, false, true},
		{dicer.RestartUnlessStopped, true, false},
		{dicer.RestartAlways, false, true},
		{dicer.RestartAlways, true, true},
	}
	for _, tt := range tests {
		if got := (dicer.RestartPolicy{Mode: tt.mode}).StartsOnBoot(tt.stoppedByUser); got != tt.want {
			t.Errorf("%s, stopped by user %v: startsOnBoot = %v, want %v", tt.mode, tt.stoppedByUser, got, tt.want)
		}
	}
}
