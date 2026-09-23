// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"testing"
	"time"
)

func TestHealthCheckValidate(t *testing.T) {
	valid := []HealthCheck{
		{Exec: []string{"pg_isready"}},
		{HTTP: &HTTPProbe{Port: 3000, Path: "/api/health"}},
		{HTTP: &HTTPProbe{Port: 80}},
		{TCP: &TCPProbe{Port: 5432}, Interval: time.Second, Retries: 1},
		{Disabled: true},
	}
	for _, c := range valid {
		if err := c.Validate(); err != nil {
			t.Errorf("Validate(%v) = %v, want valid", c, err)
		}
	}

	invalid := []HealthCheck{
		{},
		{Exec: []string{"true"}, TCP: &TCPProbe{Port: 1}},
		{HTTP: &HTTPProbe{Port: 0}},
		{TCP: &TCPProbe{Port: 70000}},
		{HTTP: &HTTPProbe{Port: 80, Path: "health"}},
		{Exec: []string{"true"}, Interval: -time.Second},
		{Exec: []string{"true"}, Retries: -1},
	}
	for _, c := range invalid {
		if err := c.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want an error", c)
		}
	}
}

func TestEffectiveHealthCheck(t *testing.T) {
	own := &HealthCheck{TCP: &TCPProbe{Port: 1}}
	image := &HealthCheck{Exec: []string{"true"}}

	if got := EffectiveHealthCheck(own, image); got == nil || got.TCP == nil {
		t.Errorf("EffectiveHealthCheck(own, image) = %v, want the instance's own", got)
	}
	if got := EffectiveHealthCheck(nil, image); got == nil || len(got.Exec) == 0 {
		t.Errorf("EffectiveHealthCheck(nil, image) = %v, want the image's", got)
	}
	if got := EffectiveHealthCheck(&HealthCheck{Disabled: true}, image); got != nil {
		t.Errorf("EffectiveHealthCheck(disabled, image) = %v, want none", got)
	}
	if got := EffectiveHealthCheck(nil, &HealthCheck{Disabled: true}); got != nil {
		t.Errorf("EffectiveHealthCheck(nil, disabled) = %v, want none", got)
	}
	if got := EffectiveHealthCheck(nil, nil); got != nil {
		t.Errorf("EffectiveHealthCheck(nil, nil) = %v, want none", got)
	}

	got := EffectiveHealthCheck(nil, image)
	if got.Interval != DefaultHealthInterval || got.Timeout != DefaultHealthTimeout ||
		got.StartPeriod != 0 || got.Retries != DefaultHealthRetries {
		t.Errorf("Effective = %+v, want the defaults filled in", got)
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
	for want, c := range tests {
		if got := c.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}
