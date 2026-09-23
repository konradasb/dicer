// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestHealthCheckRoundTrip(t *testing.T) {
	for name, p := range map[string]*dicerdv1.HealthCheck{
		"exec": {
			Probe:    &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{Command: []string{"pg_isready"}}},
			Interval: durationpb.New(time.Minute),
			Retries:  5,
		},
		"http": {
			Probe:       &dicerdv1.HealthCheck_Http{Http: &dicerdv1.HealthCheckHTTP{Port: 3000, Path: "/api/health"}},
			StartPeriod: durationpb.New(time.Minute),
		},
		"tcp": {
			Probe:   &dicerdv1.HealthCheck_Tcp{Tcp: &dicerdv1.HealthCheckTCP{Port: 5432}},
			Timeout: durationpb.New(time.Second),
		},
		"disabled": {Disabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := healthCheckFromProto(p)
			if err != nil {
				t.Fatalf("healthCheckFromProto: %v", err)
			}
			if got := healthCheckToProto(c); !proto.Equal(got, p) {
				t.Errorf("round trip = %v, want %v", got, p)
			}
		})
	}
}

func TestHealthCheckFromProtoRejects(t *testing.T) {
	for name, p := range map[string]*dicerdv1.HealthCheck{
		"no probe":   {Retries: 3},
		"bad port":   {Probe: &dicerdv1.HealthCheck_Tcp{Tcp: &dicerdv1.HealthCheckTCP{Port: 0}}},
		"bad path":   {Probe: &dicerdv1.HealthCheck_Http{Http: &dicerdv1.HealthCheckHTTP{Port: 80, Path: "health"}}},
		"no command": {Probe: &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{}}},
		"disabled, and something else": {
			Disabled: true,
			Probe:    &dicerdv1.HealthCheck_Tcp{Tcp: &dicerdv1.HealthCheckTCP{Port: 1}},
		},
	} {
		if _, err := healthCheckFromProto(p); !errors.Is(err, errdefs.ErrInvalidArgument) {
			t.Errorf("%s: healthCheckFromProto = %v, want InvalidArgument", name, err)
		}
	}

	if c, err := healthCheckFromProto(nil); c != nil || err != nil {
		t.Errorf("healthCheckFromProto(nil) = %v, %v; want no check", c, err)
	}
}

func TestHealthToProto(t *testing.T) {
	at := time.Now()
	check := types.HealthCheck{TCP: &types.TCPProbe{Port: 5432}}.WithDefaults()

	got := healthToProto(check, types.Health{
		Status: types.HealthUnhealthy, FailingStreak: 3, LastCheck: at, LastOutput: "connection refused",
	})

	if got.GetStatus() != dicerdv1.HealthStatus_HEALTH_STATUS_UNHEALTHY || got.GetFailingStreak() != 3 || got.GetLastOutput() != "connection refused" ||
		!got.GetLastCheckTime().AsTime().Equal(at) {
		t.Errorf("healthToProto = %v", got)
	}
	if got.GetCheck().GetTcp().GetPort() != 5432 || got.GetCheck().GetInterval().AsDuration() != types.DefaultHealthInterval {
		t.Errorf("check = %v, want the one being run, defaults included", got.GetCheck())
	}

	if got := healthToProto(check, types.NewHealth()); got.GetLastCheckTime() != nil {
		t.Errorf("a check not yet run reports a last check at %v", got.GetLastCheckTime())
	}
}
