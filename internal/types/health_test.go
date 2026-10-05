// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"errors"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
)

func TestHealthCheckValidate(t *testing.T) {
	tests := []struct {
		name    string
		check   HealthCheck
		wantErr bool
	}{
		{name: "exec", check: HealthCheck{Exec: []string{"pg_isready"}}},
		{name: "http with path", check: HealthCheck{HTTP: &HTTPProbe{Port: 3000, Path: "/api/health"}}},
		{name: "http without path", check: HealthCheck{HTTP: &HTTPProbe{Port: 80}}},
		{name: "tcp with timings", check: HealthCheck{TCP: &TCPProbe{Port: 5432}, Interval: time.Second, Retries: 1}},
		{name: "disabled", check: HealthCheck{Disabled: true}},
		{name: "no probe", check: HealthCheck{}, wantErr: true},
		{name: "two probes", check: HealthCheck{Exec: []string{"true"}, TCP: &TCPProbe{Port: 1}}, wantErr: true},
		{name: "port zero", check: HealthCheck{HTTP: &HTTPProbe{Port: 0}}, wantErr: true},
		{name: "port too high", check: HealthCheck{TCP: &TCPProbe{Port: 70000}}, wantErr: true},
		{name: "relative path", check: HealthCheck{HTTP: &HTTPProbe{Port: 80, Path: "health"}}, wantErr: true},
		{name: "negative interval", check: HealthCheck{Exec: []string{"true"}, Interval: -time.Second}, wantErr: true},
		{name: "negative retries", check: HealthCheck{Exec: []string{"true"}, Retries: -1}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check.Validate()
			if tt.wantErr && !errors.Is(err, errdefs.ErrInvalidArgument) {
				t.Errorf("Validate(%+v) = %v, want an invalid argument error", tt.check, err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate(%+v) = %v, want nil", tt.check, err)
			}
		})
	}
}

func TestEffectiveHealthCheck(t *testing.T) {
	own := &HealthCheck{TCP: &TCPProbe{Port: 1}}
	image := &HealthCheck{Exec: []string{"true"}}
	disabled := &HealthCheck{Disabled: true}

	tests := []struct {
		name     string
		instance *HealthCheck
		image    *HealthCheck
		want     *HealthCheck // nil means none
	}{
		{name: "instance's own wins", instance: own, image: image, want: own},
		{name: "image's when the instance has none", image: image, want: image},
		{name: "instance disables the image's", instance: disabled, image: image},
		{name: "image's disabled", image: disabled},
		{name: "neither"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveHealthCheck(tt.instance, tt.image)
			if tt.want == nil {
				if got != nil {
					t.Errorf("EffectiveHealthCheck() = %v, want none", got)
				}
				return
			}
			if got == nil || got.String() != tt.want.String() {
				t.Errorf("EffectiveHealthCheck() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveHealthCheckFillsInDefaults(t *testing.T) {
	got := EffectiveHealthCheck(nil, &HealthCheck{Exec: []string{"true"}})
	if got.Interval != DefaultHealthCheckInterval || got.Timeout != DefaultHealthCheckTimeout ||
		got.StartPeriod != 0 || got.Retries != DefaultHealthCheckRetries {
		t.Errorf("EffectiveHealthCheck() = %+v, want the defaults filled in", got)
	}
}

func TestHealthCheckString(t *testing.T) {
	tests := map[string]HealthCheck{
		"exec pg_isready -q":    {Exec: []string{"pg_isready", "-q"}},
		"http :3000/api/health": {HTTP: &HTTPProbe{Port: 3000, Path: "/api/health"}},
		"http :80/":             {HTTP: &HTTPProbe{Port: 80}},
		"tcp :5432":             {TCP: &TCPProbe{Port: 5432}},
		"disabled":              {Disabled: true},
	}

	for want, check := range tests {
		t.Run(want, func(t *testing.T) {
			if got := check.String(); got != want {
				t.Errorf("String() = %q, want %q", got, want)
			}
		})
	}
}
