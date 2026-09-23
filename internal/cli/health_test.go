// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
)

func TestHealthCheckFlags(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want *dicer.HealthCheck
	}{
		{name: "none", argv: nil, want: nil},
		{
			name: "command, run by a shell",
			argv: []string{"--health-cmd", "curl -f localhost || exit 1", "--health-retries", "5"},
			want: &dicer.HealthCheck{
				Exec:    []string{"/bin/sh", "-c", "curl -f localhost || exit 1"},
				Retries: 5,
			},
		},
		{
			name: "http",
			argv: []string{"--health-http", "3000/api/health", "--health-interval", "30s"},
			want: &dicer.HealthCheck{
				HTTP:     &dicer.HTTPProbe{Port: 3000, Path: "/api/health"},
				Interval: 30 * time.Second,
			},
		},
		{
			name: "http, on /",
			argv: []string{"--health-http", "8080"},
			want: &dicer.HealthCheck{HTTP: &dicer.HTTPProbe{Port: 8080}},
		},
		{
			name: "tcp",
			argv: []string{"--health-tcp", "5432", "--health-start-period", "1m", "--health-timeout", "2s"},
			want: &dicer.HealthCheck{
				TCP:         &dicer.TCPProbe{Port: 5432},
				StartPeriod: time.Minute,
				Timeout:     2 * time.Second,
			},
		},
		{name: "disabled", argv: []string{"--no-healthcheck"}, want: &dicer.HealthCheck{Disabled: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runBuild(t, append([]string{"web", "--image", "nginx"}, tt.argv...)...)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !reflect.DeepEqual(got.HealthCheck, tt.want) {
				t.Errorf("health check = %+v, want %+v", got.HealthCheck, tt.want)
			}
		})
	}
}

func TestHealthCheckFlagsRejected(t *testing.T) {
	for name, argv := range map[string][]string{
		"two probes":            {"--health-cmd", "true", "--health-tcp", "80"},
		"timing, no probe":      {"--health-interval", "5s"},
		"disabled, and a probe": {"--no-healthcheck", "--health-tcp", "80"},
		"bad http target":       {"--health-http", "api/health"},
	} {
		if _, err := runBuild(t, append([]string{"web", "--image", "nginx"}, argv...)...); err == nil {
			t.Errorf("%s: build accepted %v", name, argv)
		}
	}
}

func TestHealthCheckFromSpecFile(t *testing.T) {
	got, err := runBuild(t, "--file", writeSpec(t, `
name: web
image: nginx
healthcheck:
  http: 80/healthz
  interval: 15s
  retries: 2
`))
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	want := &dicer.HealthCheck{
		HTTP:     &dicer.HTTPProbe{Port: 80, Path: "/healthz"},
		Interval: 15 * time.Second,
		Retries:  2,
	}
	if !reflect.DeepEqual(got.HealthCheck, want) {
		t.Errorf("health check = %+v, want %+v", got.HealthCheck, want)
	}
}

func TestHealthRendering(t *testing.T) {
	started := time.Now().Add(-3 * time.Minute)
	check := &dicer.HealthCheck{
		HTTP:     &dicer.HTTPProbe{Port: 3000, Path: "/api/health"},
		Interval: 10 * time.Second,
		Timeout:  5 * time.Second,
		Retries:  3,
	}

	tests := []struct {
		health *dicer.Health
		status string
		lines  []string
	}{
		{nil, "Up 3 minutes", nil},
		{
			&dicer.Health{Status: dicer.HealthStarting},
			"Up 3 minutes (health: starting)",
			[]string{"starting", "http :3000/api/health every 10s", "timeout 5s, 3 retries"},
		},
		{
			&dicer.Health{Status: dicer.HealthHealthy, LastOutput: "200 OK"},
			"Up 3 minutes (healthy)",
			[]string{"healthy", "http :3000/api/health every 10s", "timeout 5s, 3 retries"},
		},
		{
			&dicer.Health{Status: dicer.HealthUnhealthy, FailingStreak: 3, LastOutput: "503 Service Unavailable\nmore"},
			"Up 3 minutes (unhealthy)",
			[]string{
				"unhealthy, 3 checks failed in a row", "last probe: 503 Service Unavailable",
				"http :3000/api/health every 10s", "timeout 5s, 3 retries",
			},
		},
	}
	for _, tt := range tests {
		inst := dicer.Instance{Status: dicer.InstanceStatus{
			State: dicer.StateRunning, StartedAt: started, Health: tt.health,
		}}
		if tt.health != nil {
			inst.Status.HealthCheck = check
		}
		if got := instanceStatus(inst); got != tt.status {
			t.Errorf("status = %q, want %q", got, tt.status)
		}
		if got := healthLines(inst, palette{}); !slices.Equal(got, tt.lines) {
			t.Errorf("health lines = %q, want %q", got, tt.lines)
		}
	}

	// A stopped instance shows the check it is configured with.
	stopped := dicer.Instance{
		Spec:   dicer.InstanceSpec{HealthCheck: &dicer.HealthCheck{Disabled: true}},
		Status: dicer.InstanceStatus{State: dicer.StateStopped},
	}
	if got := healthLines(stopped, palette{}); !slices.Equal(got, []string{"disabled"}) {
		t.Errorf("health of a stopped instance = %q, want its configured check", got)
	}
}
