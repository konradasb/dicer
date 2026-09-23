// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// Health monitors are started by supervise and stopped by forget. Probes run
// in the guest agent over vsock. An unhealthy instance is stopped only if its
// restart policy would restart it; otherwise it is reported and left running.

// agentGrace is added to a probe's timeout for the vsock round trip.
const agentGrace = 2 * time.Second

// healthMonitor holds what an instance's health checks have found.
type healthMonitor struct {
	check     types.HealthCheck
	startedAt time.Time

	// warnedOutdated suppresses repeated warnings about an agent that
	// cannot probe. Only the monitor goroutine uses it.
	warnedOutdated bool

	mu    sync.Mutex
	state types.Health
}

func newHealthMonitor(check types.HealthCheck, startedAt time.Time) *healthMonitor {
	return &healthMonitor{check: check, startedAt: startedAt, state: types.NewHealth()}
}

// observe adds a probe's result and returns the state before and after.
func (h *healthMonitor) observe(r probeResult) (before, after types.Health) {
	h.mu.Lock()
	defer h.mu.Unlock()

	before = h.state
	h.state = observe(h.state, h.check, r, r.At.Sub(h.startedAt))

	return before, h.state
}

// snapshot returns the check and what it has found so far.
func (h *healthMonitor) snapshot() (types.HealthCheck, types.Health) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.check, h.state
}

// Health returns an instance's check and its findings, or false if it is not
// being monitored.
func (m *Manager) Health(inst types.InstanceSpec) (types.HealthCheck, types.Health, bool) {
	s := m.supervision(inst.ID)
	if s == nil || s.health == nil {
		return types.HealthCheck{}, types.Health{}, false
	}
	check, state := s.health.snapshot()
	return check, state, true
}

// monitor probes an instance's health until ctx is done or the Manager
// closes. Each probe starts an interval after the previous one finished.
func (m *Manager) monitor(ctx context.Context, inst types.InstanceSpec, p *process.Process, vsockPath string, h *healthMonitor) {
	timer := time.NewTimer(h.check.Interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.closing:
			return
		case <-timer.C:
		}

		m.probeOnce(ctx, inst, p, vsockPath, h)
		timer.Reset(h.check.Interval)
	}
}

// probeOnce runs one probe, records the result and acts on unhealthy.
func (m *Manager) probeOnce(ctx context.Context, inst types.InstanceSpec, p *process.Process, vsockPath string, h *healthMonitor) {
	// A paused guest cannot answer, and has not failed for it.
	if rt, err := m.Runtime(inst); err != nil || rt.State != types.StateRunning {
		return
	}

	result, err := m.probe(ctx, vsockPath, h.check)
	switch {
	case ctx.Err() != nil:
		return
	case status.Code(err) == codes.Unimplemented:
		// The guest's agent cannot probe; that is not a failure.
		if !h.warnedOutdated {
			m.logger.WarnContext(ctx, "the guest agent cannot run health checks until the instance restarts",
				"instance", inst.Name)
			h.warnedOutdated = true
		}
		return
	}

	before, after := h.observe(result)
	if after.Status != before.Status {
		m.logger.InfoContext(ctx, "instance health changed", "instance", inst.Name,
			"health", after.Status, "check", h.check.String(), "output", firstLine(after.LastOutput))
		m.recordHealth(inst, h.check, after)
	}
	// Checked on every probe: the restart policy may have changed.
	if after.Status == types.HealthUnhealthy {
		m.handleUnhealthy(ctx, inst, p, h.check, after)
	}
}

// probeGuest runs one probe in the guest. A failing probe is a result; the
// error is for an agent that could not be reached.
func probeGuest(ctx context.Context, vsockPath string, check types.HealthCheck) (probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, check.Timeout+agentGrace)
	defer cancel()

	resp, err := askAgent(ctx, vsockPath, probeRequest(check))
	if err != nil {
		return probeResult{Output: "the guest agent did not answer: " + err.Error(), At: time.Now()}, err
	}
	return probeResult{Healthy: resp.GetHealthy(), Output: resp.GetOutput(), At: time.Now()}, nil
}

func askAgent(ctx context.Context, vsockPath string, req *diceragentv1.ProbeRequest) (*diceragentv1.ProbeResponse, error) {
	agent, closeAgent, err := dialAgent(vsockPath)
	if err != nil {
		return nil, err
	}
	defer closeAgent()

	return agent.Probe(ctx, req)
}

// probeRequest is check as the agent takes it.
func probeRequest(check types.HealthCheck) *diceragentv1.ProbeRequest {
	req := &diceragentv1.ProbeRequest{Timeout: durationpb.New(check.Timeout)}

	switch {
	case len(check.Exec) > 0:
		req.Probe = &diceragentv1.ProbeRequest_Exec{Exec: &diceragentv1.ExecProbe{Command: check.Exec}}
	case check.HTTP != nil:
		req.Probe = &diceragentv1.ProbeRequest_Http{Http: &diceragentv1.HTTPProbe{
			Port: uint32(check.HTTP.Port), Path: check.HTTP.Path,
		}}
	case check.TCP != nil:
		req.Probe = &diceragentv1.ProbeRequest_Tcp{Tcp: &diceragentv1.TCPProbe{Port: uint32(check.TCP.Port)}}
	}
	return req
}

// handleUnhealthy stops an unhealthy instance and ends it as failed, if its
// restart policy would restart it.
func (m *Manager) handleUnhealthy(ctx context.Context, inst types.InstanceSpec, p *process.Process, check types.HealthCheck, state types.Health) {
	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	if m.vmm(inst.ID) != p {
		// Stopped, deleted or replaced while we waited for the lock.
		return
	}
	if current, err := m.definitions.GetInstance(inst.ID); err == nil {
		inst = current
	}
	rt, err := m.Runtime(inst)
	if err != nil || rt.State != types.StateRunning {
		return
	}

	exit := failedExit(fmt.Errorf("health check %q failed %d times in a row: %s",
		check.String(), state.FailingStreak, firstLine(state.LastOutput)))
	if !decide(inst.Restart, exit, rt.RestartCount, time.Since(rt.StartedAt)).restart {
		return
	}

	m.logger.WarnContext(ctx, "instance is unhealthy, stopping it for its restart policy",
		"instance", inst.Name, "restart_policy", inst.Restart.String())

	// Stopping the VMM cancels this monitor's ctx.
	ctx = context.WithoutCancel(ctx)
	m.stopVMM(ctx, inst, rt, true)
	m.ended(ctx, inst, rt, exit)
}

// recordHealth records a healthy or unhealthy verdict.
func (m *Manager) recordHealth(inst types.InstanceSpec, check types.HealthCheck, state types.Health) {
	switch state.Status {
	case types.HealthHealthy:
		m.record(inst, types.ActionHealthy,
			fmt.Sprintf("Health check %q passed: %s", check.String(), firstLine(state.LastOutput)), nil)
	case types.HealthUnhealthy:
		m.record(inst, types.ActionUnhealthy,
			fmt.Sprintf("Health check %q failed %d times in a row: %s",
				check.String(), state.FailingStreak, firstLine(state.LastOutput)),
			map[string]string{"failing_streak": strconv.Itoa(state.FailingStreak)})
	case types.HealthStarting:
	}
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// probeResult is what one probe found.
type probeResult struct {
	Healthy bool

	Output string

	At time.Time
}

// observe returns the health after a probe. Failures during the start period
// do not count against the retries.
func observe(h types.Health, c types.HealthCheck, r probeResult, sinceStart time.Duration) types.Health {
	h.LastCheck = r.At
	h.LastOutput = guest.TruncateOutput(r.Output)

	if r.Healthy {
		h.Status = types.HealthHealthy
		h.FailingStreak = 0

		return h
	}

	if sinceStart < c.StartPeriod {
		return h
	}

	h.FailingStreak++
	if h.FailingStreak >= c.Retries {
		h.Status = types.HealthUnhealthy
	}

	return h
}
