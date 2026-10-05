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
	grpcstatus "google.golang.org/grpc/status"

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
	h.manager.probe = probe.probe

	check := quickCheck
	h.instance.HealthCheck = &check
	h.setRestart(t, restart)
	return h, probe
}

// waitForHealth polls until the instance's health is want.
func (h *harness) waitForHealth(t *testing.T, want types.HealthStatus) types.Health {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, health, ok := h.manager.Health(h.instance); ok && health.Status == want {
			return health
		}
		if time.Now().After(deadline) {
			_, health, ok := h.manager.Health(h.instance)
			t.Fatalf("health = %+v (monitored: %v), want %s", health, ok, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHealthyInstanceIsReportedHealthy(t *testing.T) {
	h, _ := monitored(t, types.RestartPolicy{}, true)
	h.start(t)

	health := h.waitForHealth(t, types.HealthStatusHealthy)
	if health.LastOutput != "ok" || health.LastCheck.IsZero() {
		t.Errorf("health = %+v, want the last probe's output and time", health)
	}

	check, _, _ := h.manager.Health(h.instance)
	if check.String() != "exec true" {
		t.Errorf("check = %s, want the instance's", check)
	}
}

// types.Usage counts checked instances by verdict, naming every verdict.
func TestUsageCountsHealth(t *testing.T) {
	h, _ := monitored(t, types.RestartPolicy{}, true)
	h.start(t)
	h.waitForHealth(t, types.HealthStatusHealthy)

	got := h.manager.Usage().ByHealth
	want := map[types.HealthStatus]int{types.HealthStatusStarting: 0, types.HealthStatusHealthy: 1, types.HealthStatusUnhealthy: 0}
	if !maps.Equal(got, want) {
		t.Errorf("ByHealth = %v, want %v", got, want)
	}
}

// Unhealthy is a failure to the restart policy: the VM is stopped and the
// policy restarts it.
func TestUnhealthyInstanceIsRestarted(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{Mode: types.RestartModeAlways}, false)
	h.restartAtOnce()
	h.start(t)
	first := h.starter.vmm()

	h.waitForVMMs(t, 2)
	probe.set(true, nil)
	status := h.waitForState(t, types.InstanceStateRunning)

	select {
	case <-first.Done():
	default:
		t.Error("the unhealthy instance's VMM is still running")
	}
	if status.RestartCount != 1 {
		t.Errorf("restart count = %d, want 1", status.RestartCount)
	}
	if n := h.hostNetwork.cancelledTeardowns.Load(); n > 0 {
		t.Errorf("%d network teardowns were asked for with a cancelled context", n)
	}
	h.waitForHealth(t, types.HealthStatusHealthy)
}

// Without a policy that would restart it, an unhealthy instance is reported
// and left running: stopping it would only make things worse.
func TestUnhealthyInstanceWithoutRestartPolicyKeepsRunning(t *testing.T) {
	for name, policy := range map[string]types.RestartPolicy{
		"no":                   {Mode: types.RestartModeNo},
		"retries already used": {Mode: types.RestartModeOnFailure, MaxRetries: 1},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := monitored(t, policy, false)
			h.start(t)
			if policy.MaxRetries > 0 {
				forceRestartCount(t, h, policy.MaxRetries)
			}

			health := h.waitForHealth(t, types.HealthStatusUnhealthy)
			if health.FailingStreak < quickCheck.Retries || !strings.Contains(health.LastOutput, "refused") {
				t.Errorf("health = %+v, want the failures and what the probe said", health)
			}

			time.Sleep(50 * time.Millisecond)
			if status := h.status(t); status.State != types.InstanceStateRunning || h.starter.vmmCount() != 1 {
				t.Errorf("state = %s with %d VMMs launched, want the first still running", status.State, h.starter.vmmCount())
			}
		})
	}
}

// forceRestartCount records the running instance as having been restarted
// n times in a row.
func forceRestartCount(t *testing.T, h *harness, n int) {
	t.Helper()

	lock := h.manager.lock(h.instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := h.manager.Status(h.instance)
	if err != nil {
		t.Fatal(err)
	}
	status.RestartCount = n
	if err := h.manager.writeStatus(status); err != nil {
		t.Fatal(err)
	}
}

// Whatever ends the run ends its health checks: nothing is left probing a
// VM that is gone.
func TestStopEndsHealthChecks(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{}, true)
	h.start(t)
	h.waitForHealth(t, types.HealthStatusHealthy)

	if err := h.manager.Stop(t.Context(), h.instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, _, ok := h.manager.Health(h.instance); ok {
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
	h.waitForHealth(t, types.HealthStatusHealthy)

	closed := make(chan struct{})
	go func() {
		h.manager.Close()
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
	h, probe := monitored(t, types.RestartPolicy{Mode: types.RestartModeAlways}, false)
	h.start(t)
	if err := h.manager.Pause(t.Context(), h.instance); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// A probe already on its way when the guest was paused finishes.
	time.Sleep(2 * quickCheck.Interval)

	before := probe.count()
	time.Sleep(50 * time.Millisecond)
	if after := probe.count(); after != before {
		t.Errorf("%d probes while paused, want none", after-before)
	}
	if status := h.status(t); status.State != types.InstanceStatePaused {
		t.Errorf("state = %s, want Paused", status.State)
	}
}

// A guest whose agent predates health checks cannot be checked, which is no
// evidence against it.
func TestOutdatedAgentIsNotUnhealthy(t *testing.T) {
	h, probe := monitored(t, types.RestartPolicy{Mode: types.RestartModeAlways}, false)
	probe.set(false, grpcstatus.Error(codes.Unimplemented, "unknown method Probe"))
	h.start(t)

	for probe.count() < 5 {
		time.Sleep(5 * time.Millisecond)
	}
	if _, health, _ := h.manager.Health(h.instance); health.Status != types.HealthStatusStarting {
		t.Errorf("health = %s, want starting", health.Status)
	}
	if status := h.status(t); status.State != types.InstanceStateRunning {
		t.Errorf("state = %s, want Running", status.State)
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
			images, ok := h.manager.images.(*fakeImages)
			if !ok {
				t.Fatalf("images is %T", h.manager.images)
			}
			images.held = &types.Image{
				Name: "img", Digest: "sha256:aaaa", DiskPath: images.diskPath,
				Entrypoint: []string{"/bin/sh"}, HealthCheck: imageCheck,
			}
			h.instance.HealthCheck = tt.own
			h.definitions.instances[h.instance.Name] = h.instance
			h.start(t)

			check, _, ok := h.manager.Health(h.instance)
			if ok != tt.monitored {
				t.Fatalf("monitored = %v, want %v", ok, tt.monitored)
			}
			if ok && (check.TCP == nil || check.Interval != types.DefaultHealthCheckInterval) {
				t.Errorf("check = %+v, want the image's, with the defaults", check)
			}
		})
	}
}

// TestHealthAfter is the rule a run of failures is judged by: a failure inside
// the start period is recorded but does not count, a success counts at once,
// and the retries decide when a workload is unhealthy.
func TestHealthAfter(t *testing.T) {
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
			wantStatus: types.HealthStatusStarting,
		},
		{
			name:       "one pass is healthy",
			results:    []probeResult{{Healthy: true, At: at}},
			sinceStart: time.Hour,
			wantStatus: types.HealthStatusHealthy,
		},
		{
			name:       "failures short of the retries are not yet a verdict",
			results:    []probeResult{{At: at}, {At: at}},
			sinceStart: time.Hour,
			wantStatus: types.HealthStatusStarting,
			wantStreak: 2,
		},
		{
			name:       "the retries in a row are unhealthy",
			results:    []probeResult{{At: at}, {At: at}, {At: at}},
			sinceStart: time.Hour,
			wantStatus: types.HealthStatusUnhealthy,
			wantStreak: 3,
		},
		{
			name:       "failures in the start period do not count",
			results:    []probeResult{{At: at}, {At: at}, {At: at}, {At: at}},
			sinceStart: time.Second,
			wantStatus: types.HealthStatusStarting,
		},
		{
			name:       "a pass clears the streak",
			results:    []probeResult{{At: at}, {At: at}, {Healthy: true, At: at}},
			sinceStart: time.Hour,
			wantStatus: types.HealthStatusHealthy,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			health := types.NewHealth()
			for _, r := range tc.results {
				health = healthAfter(health, check, r, tc.sinceStart)
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
func TestHealthAfterTakesASuccessInTheStartPeriod(t *testing.T) {
	health := healthAfter(types.NewHealth(), types.HealthCheck{Retries: 3, StartPeriod: time.Hour},
		probeResult{Healthy: true, At: time.Now()}, time.Second)

	if health.Status != types.HealthStatusHealthy {
		t.Errorf("status = %q, want healthy", health.Status)
	}
}

// What a probe said is kept, bounded, and cut on a rune boundary: the output
// is the guest's to choose, so its size is not to be trusted.
func TestHealthAfterKeepsWhatTheProbeSaid(t *testing.T) {
	at := time.Now()
	health := healthAfter(types.NewHealth(), types.HealthCheck{Retries: 1},
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
