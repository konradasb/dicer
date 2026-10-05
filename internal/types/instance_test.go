// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

// TestOnlyListedHypervisorTypesAreValid checks that every hypervisor type
// HypervisorTypes lists is valid, and one it does not list is not.
func TestOnlyListedHypervisorTypesAreValid(t *testing.T) {
	for _, hypervisorType := range HypervisorTypes() {
		if !hypervisorType.Valid() {
			t.Errorf("%s is listed but not valid", hypervisorType)
		}
	}
	if HypervisorType("qemu").Valid() {
		t.Error("an unlisted type is valid")
	}
}

func TestInstanceStateCanTransitionTo(t *testing.T) {
	tests := []struct {
		from, to InstanceState
		want     bool
	}{
		{InstanceStateStopped, InstanceStateStarting, true},
		{InstanceStateStarting, InstanceStateRunning, true},
		{InstanceStateRunning, InstanceStatePaused, true},
		{InstanceStatePaused, InstanceStateRunning, true},
		{InstanceStateRunning, InstanceStateStopping, true},
		{InstanceStateStopping, InstanceStateStopped, true},
		{InstanceStateFailed, InstanceStateStarting, true},
		{InstanceStateFailed, InstanceStateStopping, true},
		// An instance that ends is restarted, or left stopped or failed.
		{InstanceStateRunning, InstanceStateRestarting, true},
		{InstanceStatePaused, InstanceStateRestarting, true},
		{InstanceStateRunning, InstanceStateStopped, true},
		{InstanceStateRestarting, InstanceStateStarting, true},
		{InstanceStateRestarting, InstanceStateStopping, true},
		{InstanceStateStarting, InstanceStateRestarting, true},
		// A stopped instance cannot jump straight to running.
		{InstanceStateStopped, InstanceStateRunning, false},
		{InstanceStateStopped, InstanceStatePaused, false},
		{InstanceStateRunning, InstanceStateStarting, false},
		// A restart does not skip starting.
		{InstanceStateRestarting, InstanceStateRunning, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+" to "+string(tt.to), func(t *testing.T) {
			if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
				t.Errorf("CanTransitionTo = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestartPolicyValidate(t *testing.T) {
	valid := []RestartPolicy{
		{},
		{Mode: RestartModeNo},
		{Mode: RestartModeOnFailure},
		{Mode: RestartModeOnFailure, MaxRetries: 5},
		{Mode: RestartModeUnlessStopped},
		{Mode: RestartModeAlways},
	}
	for _, p := range valid {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v.Validate() = %v", p, err)
		}
	}

	invalid := []RestartPolicy{
		{Mode: "sometimes"},
		{Mode: RestartModeOnFailure, MaxRetries: -1},
		{Mode: RestartModeAlways, MaxRetries: 3},
		{Mode: RestartModeNo, MaxRetries: 1},
	}
	for _, p := range invalid {
		if err := p.Validate(); !errors.Is(err, errdefs.ErrInvalidArgument) {
			t.Errorf("%+v.Validate() = %v, want an invalid argument", p, err)
		}
	}
}

func TestRestartPolicyString(t *testing.T) {
	tests := []struct {
		p    RestartPolicy
		want string
	}{
		{RestartPolicy{}, "no"},
		{RestartPolicy{Mode: RestartModeNo}, "no"},
		{RestartPolicy{Mode: RestartModeOnFailure}, "on-failure"},
		{RestartPolicy{Mode: RestartModeOnFailure, MaxRetries: 5}, "on-failure:5"},
		{RestartPolicy{Mode: RestartModeUnlessStopped}, "unless-stopped"},
		{RestartPolicy{Mode: RestartModeAlways}, "always"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestRestartPolicyStartsOnBoot(t *testing.T) {
	tests := []struct {
		mode          RestartMode
		stoppedByUser bool
		want          bool
	}{
		{RestartModeNo, false, false},
		{RestartModeOnFailure, false, false},
		{RestartModeUnlessStopped, false, true},
		{RestartModeUnlessStopped, true, false},
		{RestartModeAlways, false, true},
		{RestartModeAlways, true, true},
	}
	for _, tt := range tests {
		if got := (RestartPolicy{Mode: tt.mode}).StartsOnBoot(tt.stoppedByUser); got != tt.want {
			t.Errorf("%s, stopped by user %v: StartsOnBoot = %v, want %v", tt.mode, tt.stoppedByUser, got, tt.want)
		}
	}
}
