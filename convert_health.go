// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// healthCheckToProto converts a health check; nil stays unset, which means
// the image's check applies.
func healthCheckToProto(c *HealthCheck) *dicerdv1.HealthCheck {
	if c == nil {
		return nil
	}
	if c.Disabled {
		return &dicerdv1.HealthCheck{Disabled: true}
	}

	out := &dicerdv1.HealthCheck{
		Interval:    duration(c.Interval),
		Timeout:     duration(c.Timeout),
		StartPeriod: duration(c.StartPeriod),
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

// healthCheckFromProto is healthCheckToProto backwards.
//
// It converts and does not judge: whether the result is a check the agent can
// run is HealthCheck.Validate's answer, which the daemon asks of every
// request and a client need not ask at all.

// healthCheckFromProto is healthCheckToProto backwards.
//
// It converts and does not judge: whether the result is a check the agent can
// run is HealthCheck.Validate's answer, which the daemon asks of every
// request and a client need not ask at all.
func healthCheckFromProto(p *dicerdv1.HealthCheck) *HealthCheck {
	if p == nil {
		return nil
	}
	if p.GetDisabled() {
		return &HealthCheck{Disabled: true}
	}

	c := &HealthCheck{
		Interval:    p.GetInterval().AsDuration(),
		Timeout:     p.GetTimeout().AsDuration(),
		StartPeriod: p.GetStartPeriod().AsDuration(),
		Retries:     int(p.GetRetries()),
	}
	switch probe := p.GetProbe().(type) {
	case *dicerdv1.HealthCheck_Exec:
		c.Exec = probe.Exec.GetCommand()
	case *dicerdv1.HealthCheck_Http:
		c.HTTP = &HTTPProbe{Port: int(probe.Http.GetPort()), Path: probe.Http.GetPath()}
	case *dicerdv1.HealthCheck_Tcp:
		c.TCP = &TCPProbe{Port: int(probe.Tcp.GetPort())}
	}

	return c
}

// DisabledCheckIsBare reports whether a health check switches checking off
// and then says how to check anyway, which is a request that contradicts
// itself.
//
// It is here rather than on the domain type because it is a fact about the
// message: a domain check with Disabled set simply has no probe, whereas a
// caller can put both in one message.

// healthFromProto converts what an instance's health check has found, and
// the check it is being run with.

// healthFromProto converts what an instance's health check has found, and
// the check it is being run with.
func healthFromProto(p *dicerdv1.Health) (*HealthCheck, Health) {
	if p == nil {
		return nil, Health{}
	}

	return healthCheckFromProto(p.GetCheck()), Health{
		Status:        HealthStatus(p.GetStatus()),
		FailingStreak: int(p.GetFailingStreak()),
		LastOutput:    p.GetLastOutput(),
		LastCheck:     goTime(p.GetLastCheckTime()),
	}
}
