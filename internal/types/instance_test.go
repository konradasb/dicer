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

// TestInstanceSpecValidate checks that a definition the daemon could never
// run as written is refused, and one it can is not.
func TestInstanceSpecValidate(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*InstanceSpec)
		valid  bool
	}{
		{name: "sizes alone", modify: func(*InstanceSpec) {}, valid: true},
		{name: "invalid name", modify: func(s *InstanceSpec) { s.Name = "web_1" }},
		{name: "hostname", modify: func(s *InstanceSpec) { s.Hostname = "web.example.com" }, valid: true},
		{name: "invalid hostname", modify: func(s *InstanceSpec) { s.Hostname = "-web" }},
		{name: "no image", modify: func(s *InstanceSpec) { s.ImageRef = "" }},
		{name: "no vCPUs", modify: func(s *InstanceSpec) { s.VCPUs = 0 }},
		{name: "no memory", modify: func(s *InstanceSpec) { s.MemoryBytes = 0 }},
		{name: "no disk", modify: func(s *InstanceSpec) { s.DiskBytes = 0 }},
		{
			name:   "room to grow",
			modify: func(s *InstanceSpec) { s.MaxVCPUs, s.MaxMemoryBytes = 4, 4<<30 },
			valid:  true,
		},
		{
			name:   "maximums as much as it asks for",
			modify: func(s *InstanceSpec) { s.MaxVCPUs, s.MaxMemoryBytes = 2, 2<<30 },
			valid:  true,
		},
		{name: "fewer max vCPUs than it asks for", modify: func(s *InstanceSpec) { s.MaxVCPUs = 1 }},
		{name: "less max memory than it asks for", modify: func(s *InstanceSpec) { s.MaxMemoryBytes = 1 << 30 }},
		{name: "negative max vCPUs", modify: func(s *InstanceSpec) { s.MaxVCPUs = -1 }},
		{
			name:   "max vCPUs on firecracker",
			modify: func(s *InstanceSpec) { s.HypervisorType, s.MaxVCPUs = HypervisorTypeFirecracker, 4 },
		},
		{
			name:   "max memory on firecracker",
			modify: func(s *InstanceSpec) { s.HypervisorType, s.MaxMemoryBytes = HypervisorTypeFirecracker, 4<<30 },
			valid:  true,
		},
		{
			name: "rate limits",
			modify: func(s *InstanceSpec) {
				s.DiskBytesPerSecond, s.DiskIOPS, s.UploadBytesPerSecond, s.DownloadBytesPerSecond = 1, 1, 1, 1
			},
			valid: true,
		},
		{name: "negative disk rate", modify: func(s *InstanceSpec) { s.DiskBytesPerSecond = -1 }},
		{name: "negative disk IOPS", modify: func(s *InstanceSpec) { s.DiskIOPS = -1 }},
		{name: "negative upload rate", modify: func(s *InstanceSpec) { s.UploadBytesPerSecond = -1 }},
		{name: "negative download rate", modify: func(s *InstanceSpec) { s.DownloadBytesPerSecond = -1 }},
		{name: "remove on exit", modify: func(s *InstanceSpec) { s.RemoveOnExit = true }, valid: true},
		{name: "remove on exit, never restarted", modify: removeOnExitRestarted(RestartModeNo), valid: true},
		// Deleted when it stops and started again when it stops: one of the
		// two would be quietly ignored.
		{name: "remove on exit, always restarted", modify: removeOnExitRestarted(RestartModeAlways)},
		{name: "remove on exit, restarted unless stopped", modify: removeOnExitRestarted(RestartModeUnlessStopped)},
		{name: "remove on exit, restarted on failure", modify: removeOnExitRestarted(RestartModeOnFailure)},
		{
			name:   "a restart policy alone",
			modify: func(s *InstanceSpec) { s.Restart = RestartPolicy{Mode: RestartModeAlways} },
			valid:  true,
		},
		{
			name:   "ports",
			modify: func(s *InstanceSpec) { s.Ports = []PortMapping{{HostPort: 8080, GuestPort: 80}} },
			valid:  true,
		},
		{
			name: "clashing ports",
			modify: func(s *InstanceSpec) {
				s.Ports = []PortMapping{{HostPort: 8080, GuestPort: 80}, {HostPort: 8080, GuestPort: 81}}
			},
		},
		{
			name:   "mounts",
			modify: func(s *InstanceSpec) { s.Mounts = []Mount{{Type: MountTypeTmpfs, Target: "/scratch"}} },
			valid:  true,
		},
		{
			name:   "invalid mount",
			modify: func(s *InstanceSpec) { s.Mounts = []Mount{{Type: MountTypeTmpfs, Target: "scratch"}} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := InstanceSpec{Name: "web", ImageRef: "alpine", VCPUs: 2, MemoryBytes: 2 << 30, DiskBytes: 10 << 30}
			tt.modify(&s)

			err := s.Validate()
			switch {
			case tt.valid && err != nil:
				t.Errorf("Validate = %v, want nil", err)
			case !tt.valid && !errors.Is(err, errdefs.ErrInvalidArgument):
				t.Errorf("Validate = %v, want an invalid argument", err)
			}
		})
	}
}

// removeOnExitRestarted modifies a spec to be deleted when it stops and
// restarted as mode says.
func removeOnExitRestarted(mode RestartMode) func(*InstanceSpec) {
	return func(s *InstanceSpec) { s.RemoveOnExit, s.Restart = true, RestartPolicy{Mode: mode} }
}
