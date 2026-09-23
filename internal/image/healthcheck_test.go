// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"reflect"
	"testing"
	"time"

	gcr "github.com/google/go-containerregistry/pkg/v1"

	"github.com/konradasb/dicer/internal/types"
)

func TestHealthCheckFromDocker(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *gcr.HealthConfig
		want *types.HealthCheck
	}{
		{"no config", nil, nil},
		{"empty test", &gcr.HealthConfig{}, nil},
		{
			"NONE disables the check a base image declared",
			&gcr.HealthConfig{Test: []string{"NONE"}},
			&types.HealthCheck{Disabled: true},
		},
		{
			"CMD is the command and its arguments",
			&gcr.HealthConfig{Test: []string{"CMD", "pg_isready", "-U", "postgres"}},
			&types.HealthCheck{Exec: []string{"pg_isready", "-U", "postgres"}},
		},
		{
			"CMD-SHELL is run by a shell",
			&gcr.HealthConfig{Test: []string{"CMD-SHELL", "curl -f localhost || exit 1"}},
			&types.HealthCheck{Exec: []string{"/bin/sh", "-c", "curl -f localhost || exit 1"}},
		},
		{
			"timings are carried as they were given",
			&gcr.HealthConfig{
				Test:        []string{"CMD", "true"},
				Interval:    30 * time.Second,
				Timeout:     3 * time.Second,
				StartPeriod: time.Minute,
				Retries:     5,
			},
			&types.HealthCheck{
				Exec:        []string{"true"},
				Interval:    30 * time.Second,
				Timeout:     3 * time.Second,
				StartPeriod: time.Minute,
				Retries:     5,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := healthCheckFromDocker(tc.in)
			if err != nil {
				t.Fatalf("healthCheckFromDocker: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("check = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// An unset timing is left unset, so that the check takes Dicer's defaults
// rather than Docker's when it runs.
func TestHealthCheckFromDockerLeavesUnsetTimingsAlone(t *testing.T) {
	got, err := healthCheckFromDocker(&gcr.HealthConfig{Test: []string{"CMD", "true"}})
	if err != nil {
		t.Fatalf("healthCheckFromDocker: %v", err)
	}

	if got.Interval != 0 || got.Timeout != 0 || got.StartPeriod != 0 || got.Retries != 0 {
		t.Errorf("check = %+v, want its timings left for the defaults", got)
	}
}

func TestHealthCheckFromDockerRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *gcr.HealthConfig
	}{
		{"unknown test", &gcr.HealthConfig{Test: []string{"HTTP", "8080"}}},
		{"CMD-SHELL with no command", &gcr.HealthConfig{Test: []string{"CMD-SHELL"}}},
		{"CMD-SHELL with several", &gcr.HealthConfig{Test: []string{"CMD-SHELL", "a", "b"}}},
		// A check Dicer cannot run is refused here rather than at the
		// instance's first start.
		{"negative retries", &gcr.HealthConfig{Test: []string{"CMD", "true"}, Retries: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := healthCheckFromDocker(tc.in); err == nil {
				t.Errorf("healthCheckFromDocker(%+v) was accepted", tc.in)
			}
		})
	}
}
