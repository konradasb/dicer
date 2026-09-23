// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dicer-sh/dicer"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// healthCheckFromProto converts and checks a health check. None at all is
// nil: the image's check applies.
func healthCheckFromProto(p *dicerdv1.HealthCheck) (*dicer.HealthCheck, error) {
	if p == nil {
		return nil, nil //nolint:nilnil // no check is a valid answer, not a missing one
	}

	if p.GetDisabled() {
		if p.GetProbe() != nil || p.GetInterval() != nil || p.GetTimeout() != nil ||
			p.GetStartPeriod() != nil || p.GetRetries() != 0 {
			return nil, status.Error(codes.InvalidArgument, "a disabled health check takes nothing else")
		}
		return &dicer.HealthCheck{Disabled: true}, nil
	}

	c := &dicer.HealthCheck{
		Interval:    p.GetInterval().AsDuration(),
		Timeout:     p.GetTimeout().AsDuration(),
		StartPeriod: p.GetStartPeriod().AsDuration(),
		Retries:     int(p.GetRetries()),
	}
	switch probe := p.GetProbe().(type) {
	case *dicerdv1.HealthCheck_Exec:
		c.Exec = probe.Exec.GetCommand()
	case *dicerdv1.HealthCheck_Http:
		c.HTTP = &dicer.HTTPProbe{Port: int(probe.Http.GetPort()), Path: probe.Http.GetPath()}
	case *dicerdv1.HealthCheck_Tcp:
		c.TCP = &dicer.TCPProbe{Port: int(probe.Tcp.GetPort())}
	}

	if err := c.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return c, nil
}

// healthCheckToProto converts a health check; nil stays unset.
func healthCheckToProto(c *dicer.HealthCheck) *dicerdv1.HealthCheck {
	if c == nil {
		return nil
	}
	if c.Disabled {
		return &dicerdv1.HealthCheck{Disabled: true}
	}

	out := &dicerdv1.HealthCheck{
		Interval:    optionalDuration(c.Interval),
		Timeout:     optionalDuration(c.Timeout),
		StartPeriod: optionalDuration(c.StartPeriod),
		Retries:     int32(c.Retries),
	}
	switch {
	case len(c.Exec) > 0:
		out.Probe = &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{Command: c.Exec}}
	case c.HTTP != nil:
		out.Probe = &dicerdv1.HealthCheck_Http{Http: &dicerdv1.HealthCheckHTTP{
			Port: uint32(c.HTTP.Port), Path: c.HTTP.Path,
		}}
	case c.TCP != nil:
		out.Probe = &dicerdv1.HealthCheck_Tcp{Tcp: &dicerdv1.HealthCheckTCP{Port: uint32(c.TCP.Port)}}
	}
	return out
}

// healthToProto converts what an instance's health check has found, and the
// check it is being run with.
func healthToProto(check dicer.HealthCheck, state dicer.Health) *dicerdv1.Health {
	out := &dicerdv1.Health{
		Status:        string(state.Status),
		FailingStreak: int32(state.FailingStreak),
		LastOutput:    state.LastOutput,
		Check:         healthCheckToProto(&check),
	}
	if !state.LastCheck.IsZero() {
		out.LastCheckTime = timestamppb.New(state.LastCheck)
	}
	return out
}

// optionalDuration leaves a zero duration unset, as a request that did not
// set it would.
func optionalDuration(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}
