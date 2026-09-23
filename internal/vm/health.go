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
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/process"
	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// Health checking is part of supervision. An instance whose run has a check
// gets a monitor alongside its exit watcher: started by supervise, stopped
// by forget, so that whatever ends a run -- a stop, a crash, a delete --
// ends its checking too, with nothing else to keep track of.
//
// The probes themselves run inside the guest, by its agent, over vsock; the
// monitor decides when to probe and what the results add up to. A workload
// found unhealthy is an end like any other: if the instance's restart
// policy would restart it after a failure, the VMM is stopped and the policy
// takes over, backoff and retry limit included. If not, it is reported and
// left running: stopping an instance that will not come back helps no one.

// agentGrace is how much longer than a probe's own timeout the monitor waits
// for the agent's answer, for the round trip over vsock.
const agentGrace = 2 * time.Second

// healthMonitor holds what an instance's health checks have found.
type healthMonitor struct {
	check     dicer.HealthCheck
	startedAt time.Time

	// warnedOutdated is set once the guest's agent has been found too old
	// to probe, so that it is said once. Only the monitor goroutine uses it.
	warnedOutdated bool

	mu    sync.Mutex
	state dicer.Health
}

func newHealthMonitor(check dicer.HealthCheck, startedAt time.Time) *healthMonitor {
	return &healthMonitor{check: check, startedAt: startedAt, state: dicer.NewHealth()}
}

// observe adds a probe's result, and returns the state before it and the
// state it leads to.
func (h *healthMonitor) observe(r probeResult) (before, after dicer.Health) {
	h.mu.Lock()
	defer h.mu.Unlock()

	before = h.state
	h.state = observe(h.state, h.check, r, r.At.Sub(h.startedAt))

	return before, h.state
}

// snapshot returns the check and what it has found so far.
func (h *healthMonitor) snapshot() (dicer.HealthCheck, dicer.Health) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.check, h.state
}

// Health returns the check an instance is being monitored with and what it
// has found, or false if it is not being monitored: it is not running, or
// has no check.
func (m *Manager) Health(inst dicer.InstanceSpec) (dicer.HealthCheck, dicer.Health, bool) {
	s := m.supervision(inst.ID)
	if s == nil || s.health == nil {
		return dicer.HealthCheck{}, dicer.Health{}, false
	}
	check, state := s.health.snapshot()
	return check, state, true
}

// monitor probes an instance's health until ctx is done, which forget sees
// to when the VMM p goes, or the Manager closes: the VMM outlives the daemon,
// and the next one monitors it. Each probe starts an interval after the last
// one finished, so a slow probe does not have the next queued behind it.
func (m *Manager) monitor(ctx context.Context, inst dicer.InstanceSpec, p *process.Process, vsockPath string, h *healthMonitor) {
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

// probeOnce runs one probe for monitor, records what it found, and acts on
// a verdict of unhealthy.
func (m *Manager) probeOnce(ctx context.Context, inst dicer.InstanceSpec, p *process.Process, vsockPath string, h *healthMonitor) {
	// A paused guest cannot answer, and has not failed for it.
	if rt, err := m.Runtime(inst); err != nil || rt.State != dicer.StateRunning {
		return
	}

	result, err := m.probe(ctx, vsockPath, h.check)
	switch {
	case ctx.Err() != nil:
		return
	case status.Code(err) == codes.Unimplemented:
		// A guest booted with an agent from before health checks. It
		// cannot be checked, which is no evidence it is unhealthy; its next
		// boot installs a current agent.
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
	// Acted on at every probe while unhealthy, not only on the change: the
	// restart policy may have changed since.
	if after.Status == dicer.HealthUnhealthy {
		m.handleUnhealthy(ctx, inst, p, h.check, after)
	}
}

// probeGuest runs one probe of check in the guest behind vsockPath. A probe
// that fails is a result, not an error; the error is for an agent that could
// not be asked at all, which the result also records as a failure.
func probeGuest(ctx context.Context, vsockPath string, check dicer.HealthCheck) (probeResult, error) {
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
func probeRequest(check dicer.HealthCheck) *diceragentv1.ProbeRequest {
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

// handleUnhealthy applies an instance's restart policy to a workload found
// unhealthy: if the policy would restart it after a failure, its VMM is
// stopped and the instance ends, as if it had crashed; if not, it is left
// running.
func (m *Manager) handleUnhealthy(ctx context.Context, inst dicer.InstanceSpec, p *process.Process, check dicer.HealthCheck, state dicer.Health) {
	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	if m.vmm(inst.ID) != p {
		// Stopped, deleted or replaced while we waited for the lock.
		return
	}
	// The restart policy that applies is the one the instance has now.
	if current, err := m.definitions.GetInstance(inst.ID); err == nil {
		inst = current
	}
	rt, err := m.Runtime(inst)
	if err != nil || rt.State != dicer.StateRunning {
		return
	}

	exit := failedExit(fmt.Errorf("health check %q failed %d times in a row: %s",
		check.String(), state.FailingStreak, firstLine(state.LastOutput)))
	if !decide(inst.Restart, exit, rt.RestartCount, time.Since(rt.StartedAt)).restart {
		return
	}

	m.logger.WarnContext(ctx, "instance is unhealthy, stopping it for its restart policy",
		"instance", inst.Name, "restart_policy", inst.Restart.String())

	// ctx is this monitor's, and stopping the VMM ends the monitor: the
	// stop and the teardown after it must not be cut short by that.
	ctx = context.WithoutCancel(ctx)
	m.stopVMM(ctx, inst, rt, true)
	m.ended(ctx, inst, rt, exit)
}

// recordHealth records a health check reaching a verdict. Starting is not
// one: it is where every check begins.
func (m *Manager) recordHealth(inst dicer.InstanceSpec, check dicer.HealthCheck, state dicer.Health) {
	switch state.Status {
	case dicer.HealthHealthy:
		m.record(inst, dicer.ActionHealthy,
			fmt.Sprintf("Health check %q passed: %s", check.String(), firstLine(state.LastOutput)), nil)
	case dicer.HealthUnhealthy:
		m.record(inst, dicer.ActionUnhealthy,
			fmt.Sprintf("Health check %q failed %d times in a row: %s",
				check.String(), state.FailingStreak, firstLine(state.LastOutput)),
			map[string]string{"failing_streak": strconv.Itoa(state.FailingStreak)})
	case dicer.HealthStarting:
	}
}

// firstLine is the first line of s, for a log or a reason that has room for
// one.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// probeResult is what one probe found.
type probeResult struct {
	Healthy bool

	// Output is what the probe said: a command's output, an HTTP status,
	// or why it failed.
	Output string

	At time.Time
}

// maxOutput bounds what is kept of a probe's output, which is a guest's to
// choose and so is not to be trusted to be small.
const maxOutput = 4096

// observe returns the health after a probe.
//
// A failure in the start period is recorded but does not count against the
// retries: a workload still coming up has not failed yet. A success counts
// at once, whenever it arrives.
func observe(h dicer.Health, c dicer.HealthCheck, r probeResult, sinceStart time.Duration) dicer.Health {
	h.LastCheck = r.At
	h.LastOutput = truncate(r.Output, maxOutput)

	if r.Healthy {
		h.Status = dicer.HealthHealthy
		h.FailingStreak = 0

		return h
	}

	if sinceStart < c.StartPeriod {
		return h
	}

	h.FailingStreak++
	if h.FailingStreak >= c.Retries {
		h.Status = dicer.HealthUnhealthy
	}

	return h
}

// truncate cuts s to at most n bytes, on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n]
}
