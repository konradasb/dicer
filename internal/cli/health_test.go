// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// execProbe, httpProbe and tcpProbe are health check probes, as the API takes them.
func execProbe(command ...string) *dicerdv1.HealthCheck_Exec {
	return &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{Command: command}}
}

func httpProbe(port uint32, path string) *dicerdv1.HealthCheck_Http {
	return &dicerdv1.HealthCheck_Http{Http: &dicerdv1.HealthCheckHTTP{Port: port, Path: path}}
}

func tcpProbe(port uint32) *dicerdv1.HealthCheck_Tcp {
	return &dicerdv1.HealthCheck_Tcp{Tcp: &dicerdv1.HealthCheckTCP{Port: port}}
}

func TestHealthCheckFlags(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want *dicerdv1.HealthCheck
	}{
		{name: "none", argv: nil, want: nil},
		{
			name: "command, run by a shell",
			argv: []string{"--health-cmd", "curl -f localhost || exit 1", "--health-retries", "5"},
			want: &dicerdv1.HealthCheck{Probe: execProbe("/bin/sh", "-c", "curl -f localhost || exit 1"), Retries: 5},
		},
		{
			name: "http",
			argv: []string{"--health-http", "3000/api/health", "--health-interval", "30s"},
			want: &dicerdv1.HealthCheck{Probe: httpProbe(3000, "/api/health"), Interval: durationpb.New(30 * time.Second)},
		},
		{
			name: "http, on /",
			argv: []string{"--health-http", "8080"},
			want: &dicerdv1.HealthCheck{Probe: httpProbe(8080, "")},
		},
		{
			name: "tcp",
			argv: []string{"--health-tcp", "5432", "--health-start-period", "1m", "--health-timeout", "2s"},
			want: &dicerdv1.HealthCheck{
				Probe:       tcpProbe(5432),
				StartPeriod: durationpb.New(time.Minute),
				Timeout:     durationpb.New(2 * time.Second),
			},
		},
		{name: "disabled", argv: []string{"--no-healthcheck"}, want: &dicerdv1.HealthCheck{Disabled: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runBuild(t, append([]string{"web", "--image", "nginx"}, tt.argv...)...)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !proto.Equal(got.GetHealthCheck(), tt.want) {
				t.Errorf("health check = %v, want %v", got.GetHealthCheck(), tt.want)
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

func TestHealthRendering(t *testing.T) {
	started := timestamppb.New(time.Now().Add(-3 * time.Minute))
	check := &dicerdv1.HealthCheck{
		Probe:    httpProbe(3000, "/api/health"),
		Interval: durationpb.New(10 * time.Second),
		Timeout:  durationpb.New(5 * time.Second),
		Retries:  3,
	}

	tests := []struct {
		health *dicerdv1.Health
		status string
		lines  []string
	}{
		{nil, "Up 3 minutes", nil},
		{
			&dicerdv1.Health{Status: healthStarting},
			"Up 3 minutes (health: starting)",
			[]string{"starting", "http :3000/api/health every 10s", "timeout 5s, 3 retries"},
		},
		{
			&dicerdv1.Health{Status: healthHealthy, LastOutput: "200 OK"},
			"Up 3 minutes (healthy)",
			[]string{"healthy", "http :3000/api/health every 10s", "timeout 5s, 3 retries"},
		},
		{
			&dicerdv1.Health{Status: healthUnhealthy, FailingStreak: 3, LastOutput: "503 Service Unavailable\nmore"},
			"Up 3 minutes (unhealthy)",
			[]string{
				"unhealthy, 3 checks failed in a row", "last probe: 503 Service Unavailable",
				"http :3000/api/health every 10s", "timeout 5s, 3 retries",
			},
		},
	}
	for _, tt := range tests {
		inst := &dicerdv1.Instance{State: stateRunning, StartTime: started, Health: tt.health}
		if tt.health != nil {
			inst.Health.Check = check
		}
		if got := instanceStatus(inst); got != tt.status {
			t.Errorf("status = %q, want %q", got, tt.status)
		}
		if got := healthLines(inst, palette{}); !slices.Equal(got, tt.lines) {
			t.Errorf("health lines = %q, want %q", got, tt.lines)
		}
	}

	// A stopped instance shows the check it is configured with.
	stopped := &dicerdv1.Instance{HealthCheck: &dicerdv1.HealthCheck{Disabled: true}, State: stateStopped}
	if got := healthLines(stopped, palette{}); !slices.Equal(got, []string{"disabled"}) {
		t.Errorf("health of a stopped instance = %q, want its configured check", got)
	}
}
