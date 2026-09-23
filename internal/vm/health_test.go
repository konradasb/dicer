// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"maps"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/types"
)

// quickCheck is a check that reaches a verdict in milliseconds.
var quickCheck = types.HealthCheck{
	Exec:     []string{"true"},
	Interval: 5 * time.Millisecond,
	Timeout:  5 * time.Millisecond,
	Retries:  2,
}

// monitored returns a harness whose instance is checked with quickCheck,
// probed by the returned fake.
func monitored(t *testing.T, restart types.RestartPolicy, healthy bool) (*harness, *fakeProbe) {
	t.Helper()

	h := newHarness(t)
	probe := &fakeProbe{healthy: healthy}
	h.mgr.probe = probe.probe

	check := quickCheck
	h.inst.HealthCheck = &check
	h.setRestart(t, restart)
	return h, probe
}

// waitForHealth polls until the instance's health is want.
func (h *harness) waitForHealth(t *testing.T, want types.HealthStatus) types.Health {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, state, ok := h.mgr.Health(h.inst); ok && state.Status == want {
			return state
		}
		if time.Now().After(deadline) {
			_, state, ok := h.mgr.Health(h.inst)
			t.Fatalf("health = %+v (monitored: %v), want %s", state, ok, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHealthyInstanceIsReportedHealthy(t *testing.T) {
	h, _ := monitored(t, types.RestartPolicy{}, true)
	h.start(t)

	state := h.waitForHealth(t, types.HealthHealthy)
	if state.LastOutput != "ok" || state.LastCheck.IsZero() {
		t.Errorf("state = %+v, want the last probe's output and time", state)
	}

	check, _, _ := h.mgr.Health(h.inst)
	if check.String() != "exec true" {
		t.Errorf("check = %s, want the instance's", check)
	}
}

// types.Usage counts checked instances by verdict, naming every verdict.
func TestUsageCountsHealth(t *testing.T) {
	h, _ := monitored(t, types.RestartPolicy{}, true)
	h.start(t)
	h.waitForHealth(t, types.HealthHealthy)

	got := h.mgr.Usage().ByHealth
	want := map[types.HealthStatus]int{types.HealthStarting: 0, types.HealthHealthy: 1, types.HealthUnhealthy: 0}
	if !maps.Equal(got, want) {
		t.Errorf("ByHealth = %v, want %v", got, want)
	}
}

// Unhealthy is a failure to the restart policy: the VM is stopped and the
// policy restarts it.
func TestUnhealthyInstanceIsRestarted(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{Mode: types.RestartAlways}, false)
	h.restartAtOnce()
	h.start(t)
	first := h.starter.vmm()

	h.waitForVMMs(t, 2)
	probe.set(true, nil)
	rt := h.waitForState(t, types.StateRunning)

	select {
	case <-first.Done():
	default:
		t.Error("the unhealthy instance's VMM is still running")
	}
	if rt.RestartCount != 1 {
		t.Errorf("restart count = %d, want 1", rt.RestartCount)
	}
	if n := h.hostNetwork.cancelledTeardowns.Load(); n > 0 {
		t.Errorf("%d network teardowns were asked for with a cancelled context", n)
	}
	h.waitForHealth(t, types.HealthHealthy)
}

// Without a policy that would restart it, an unhealthy instance is reported
// and left running: stopping it would only make things worse.
func TestUnhealthyInstanceWithoutRestartPolicyKeepsRunning(t *testing.T) {
	for name, policy := range map[string]types.RestartPolicy{
		"no":                   {Mode: types.RestartNo},
		"retries already used": {Mode: types.RestartOnFailure, MaxRetries: 1},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := monitored(t, policy, false)
			if policy.MaxRetries > 0 {
				h.start(t)
				forceRestartCount(t, h, policy.MaxRetries)
			} else {
				h.start(t)
			}

			state := h.waitForHealth(t, types.HealthUnhealthy)
			if state.FailingStreak < quickCheck.Retries || !strings.Contains(state.LastOutput, "refused") {
				t.Errorf("state = %+v, want the failures and what the probe said", state)
			}

			time.Sleep(50 * time.Millisecond)
			if rt := h.runtime(t); rt.State != types.StateRunning || len(h.starter.vmms) != 1 {
				t.Errorf("state = %s with %d VMMs launched, want the first still running", rt.State, len(h.starter.vmms))
			}
		})
	}
}

// forceRestartCount records the running instance as having been restarted
// n times in a row.
func forceRestartCount(t *testing.T, h *harness, n int) {
	t.Helper()

	lock := h.mgr.lock(h.inst.ID)
	lock.Lock()
	defer lock.Unlock()

	rt, err := h.mgr.Runtime(h.inst)
	if err != nil {
		t.Fatal(err)
	}
	rt.RestartCount = n
	if err := h.mgr.writeRuntime(rt); err != nil {
		t.Fatal(err)
	}
}

// Whatever ends the run ends its health checks: nothing is left probing a
// VM that is gone.
func TestStopEndsHealthChecks(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{}, true)
	h.start(t)
	h.waitForHealth(t, types.HealthHealthy)

	if err := h.mgr.Stop(t.Context(), h.inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, _, ok := h.mgr.Health(h.inst); ok {
		t.Error("a stopped instance is still monitored")
	}

	before := probe.count()
	time.Sleep(50 * time.Millisecond)
	if after := probe.count(); after != before {
		t.Errorf("%d probes after the stop, want none", after-before)
	}
}

// A daemon shutting down stops checking, and does not wait for the VMMs it
// checks, which outlive it: the next daemon checks them.
func TestCloseStopsHealthChecks(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{}, true)
	h.start(t)
	h.waitForHealth(t, types.HealthHealthy)

	closed := make(chan struct{})
	go func() {
		h.mgr.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close is still waiting on the health monitor")
	}

	before := probe.count()
	time.Sleep(50 * time.Millisecond)
	if after := probe.count(); after != before {
		t.Errorf("%d probes after Close, want none", after-before)
	}
}

// A paused guest cannot answer, and has not failed for it.
func TestPausedInstanceIsNotProbed(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{Mode: types.RestartAlways}, false)
	h.start(t)
	if err := h.mgr.Pause(t.Context(), h.inst); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// A probe already on its way when the guest was paused finishes.
	time.Sleep(2 * quickCheck.Interval)

	before := probe.count()
	time.Sleep(50 * time.Millisecond)
	if after := probe.count(); after != before {
		t.Errorf("%d probes while paused, want none", after-before)
	}
	if rt := h.runtime(t); rt.State != types.StatePaused {
		t.Errorf("state = %s, want Paused", rt.State)
	}
}

// A guest whose agent predates health checks cannot be checked, which is no
// evidence against it.
func TestOutdatedAgentIsNotUnhealthy(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{Mode: types.RestartAlways}, false)
	probe.set(false, status.Error(codes.Unimplemented, "unknown method Probe"))
	h.start(t)

	for probe.count() < 5 {
		time.Sleep(5 * time.Millisecond)
	}
	if _, state, _ := h.mgr.Health(h.inst); state.Status != types.HealthStarting {
		t.Errorf("health = %s, want starting", state.Status)
	}
	if rt := h.runtime(t); rt.State != types.StateRunning {
		t.Errorf("state = %s, want Running", rt.State)
	}
}

// An instance with no check of its own gets its image's, unless it switches
// it off.
func TestHealthCheckComesFromTheImage(t *testing.T) {
	imageCheck := &types.HealthCheck{TCP: &types.TCPProbe{Port: 5432}}

	for name, tt := range map[string]struct {
		own       *types.HealthCheck
		monitored bool
	}{
		"inherited":    {own: nil, monitored: true},
		"switched off": {own: &types.HealthCheck{Disabled: true}, monitored: false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			images, ok := h.mgr.images.(*fakeImages)
			if !ok {
				t.Fatalf("images is %T", h.mgr.images)
			}
			images.held = &types.Image{
				Name: "img", Digest: "sha256:aaaa", DiskPath: images.diskPath,
				Entrypoint: []string{"/bin/sh"}, HealthCheck: imageCheck,
			}
			h.inst.HealthCheck = tt.own
			h.definitions.instances[h.inst.Name] = h.inst
			h.start(t)

			check, _, ok := h.mgr.Health(h.inst)
			if ok != tt.monitored {
				t.Fatalf("monitored = %v, want %v", ok, tt.monitored)
			}
			if ok && (check.TCP == nil || check.Interval != types.DefaultHealthInterval) {
				t.Errorf("check = %+v, want the image's, with the defaults", check)
			}
		})
	}
}

// TestObserve is the rule a run of failures is judged by: a failure inside
// the start period is recorded but does not count, a success counts at once,
// and the retries decide when a workload is unhealthy.
func TestObserve(t *testing.T) {
	check := types.HealthCheck{Retries: 3, StartPeriod: time.Minute}
	at := time.Now()

	for _, tc := range []struct {
		name       string
		results    []probeResult
		sinceStart time.Duration
		wantStatus types.HealthStatus
		wantStreak int
	}{
		{
			name:       "a fresh check has reached no verdict",
			sinceStart: time.Hour,
			wantStatus: types.HealthStarting,
		},
		{
			name:       "one pass is healthy",
			results:    []probeResult{{Healthy: true, At: at}},
			sinceStart: time.Hour,
			wantStatus: types.HealthHealthy,
		},
		{
			name:       "failures short of the retries are not yet a verdict",
			results:    []probeResult{{At: at}, {At: at}},
			sinceStart: time.Hour,
			wantStatus: types.HealthStarting,
			wantStreak: 2,
		},
		{
			name:       "the retries in a row are unhealthy",
			results:    []probeResult{{At: at}, {At: at}, {At: at}},
			sinceStart: time.Hour,
			wantStatus: types.HealthUnhealthy,
			wantStreak: 3,
		},
		{
			name:       "failures in the start period do not count",
			results:    []probeResult{{At: at}, {At: at}, {At: at}, {At: at}},
			sinceStart: time.Second,
			wantStatus: types.HealthStarting,
		},
		{
			name:       "a pass clears the streak",
			results:    []probeResult{{At: at}, {At: at}, {Healthy: true, At: at}},
			sinceStart: time.Hour,
			wantStatus: types.HealthHealthy,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			health := types.NewHealth()
			for _, r := range tc.results {
				health = observe(health, check, r, tc.sinceStart)
			}

			if health.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", health.Status, tc.wantStatus)
			}
			if health.FailingStreak != tc.wantStreak {
				t.Errorf("failing streak = %d, want %d", health.FailingStreak, tc.wantStreak)
			}
		})
	}
}

// A success in the start period counts at once: a workload that is up is up,
// however long it was given to get there.
func TestObserveTakesASuccessInTheStartPeriod(t *testing.T) {
	health := observe(types.NewHealth(), types.HealthCheck{Retries: 3, StartPeriod: time.Hour},
		probeResult{Healthy: true, At: time.Now()}, time.Second)

	if health.Status != types.HealthHealthy {
		t.Errorf("status = %q, want healthy", health.Status)
	}
}

// What a probe said is kept, bounded, and cut on a rune boundary: the output
// is the guest's to choose, so its size is not to be trusted.
func TestObserveKeepsWhatTheProbeSaid(t *testing.T) {
	at := time.Now()
	health := observe(types.NewHealth(), types.HealthCheck{Retries: 1},
		probeResult{Output: strings.Repeat("é", guest.MaxProbeOutput), At: at}, time.Hour)

	if len(health.LastOutput) > guest.MaxProbeOutput {
		t.Errorf("output is %d bytes, want at most %d", len(health.LastOutput), guest.MaxProbeOutput)
	}
	if !utf8.ValidString(health.LastOutput) {
		t.Error("output was cut in the middle of a rune")
	}
	if !health.LastCheck.Equal(at) {
		t.Errorf("last check = %v, want %v", health.LastCheck, at)
	}
}
