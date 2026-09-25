// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestResourcesCommandIsGone(t *testing.T) {
	isolateConfig(t)

	if _, err := run(t, "resources"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("dicer resources = %v, want an unknown command", err)
	}
}

func TestPsCompactByDefault(t *testing.T) {
	instances := fakeInstances()
	instances[0].StartTime = timestamppb.New(time.Now().Add(-3 * time.Minute))
	serveInstanceDaemon(t, newFakeInstanceDaemon(instances...))

	out, err := run(t, "ps")
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, out)
	}
	header, _, _ := strings.Cut(out, "\n")
	if got := strings.Fields(header); !slices.Equal(got, []string{"NAME", "IMAGE", "STATUS", "IP", "PORTS"}) {
		t.Errorf("header = %q, want the compact columns", got)
	}
	if !strings.Contains(out, "Up 3 minutes") {
		t.Errorf("a running instance should say how long it has been up:\n%s", out)
	}

	out, err = run(t, "ps", "--wide")
	if err != nil {
		t.Fatalf("ps --wide: %v\n%s", err, out)
	}
	for _, col := range []string{"STATE", "VCPU", "MEMORY", "DISK", "NETWORK", "CREATED"} {
		if !strings.Contains(out, col) {
			t.Errorf("ps --wide is missing column %s:\n%s", col, out)
		}
	}

	// Structured output has every column, whatever a table shows.
	out, err = run(t, "ps", "--format", "{{.VCPU}} {{.State}}", "-f", "name=web")
	if err != nil || out != "2 Running\n" {
		t.Errorf("template output = %q, %v", out, err)
	}
}

func TestDebugTracesCalls(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	out, err := run(t, "--debug", "ps", "-q")
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, out)
	}
	for _, want := range []string{"debug ", "remote unix://", "ListInstances OK"} {
		if !strings.Contains(out, want) {
			t.Errorf("--debug output is missing %q:\n%s", want, out)
		}
	}

	t.Setenv(debugEnv, "1")
	if out, _ := run(t, "ps", "-q"); !strings.Contains(out, "ListInstances OK") {
		t.Errorf("$%s should trace as --debug does:\n%s", debugEnv, out)
	}
}

func TestTimeout(t *testing.T) {
	d := newFakeInstanceDaemon()
	d.hostDelay = time.Second
	serveInstanceDaemon(t, d)

	_, err := run(t, "--timeout", "50ms", "info")
	if err == nil || !strings.Contains(err.Error(), "GetHostInfo took longer than --timeout 50ms") {
		t.Errorf("err = %v, want it to name the call and the timeout", err)
	}
}

func TestDidYouMean(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	for _, args := range [][]string{{"stop", "wbe"}, {"inspect", "weeb"}} {
		_, err := run(t, args...)
		if err == nil || !strings.HasSuffix(err.Error(), " (did you mean web?)") {
			t.Errorf("%v = %v, want a suggestion of web", args, err)
		}
	}

	// Nothing close: no suggestion.
	if _, err := run(t, "stop", "zzzzzz"); err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Errorf("stop zzzzzz = %v, want a plain not found", err)
	}
}

func TestCloseNames(t *testing.T) {
	names := []string{"web", "web-2", "db", "database", "cache"}
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"wbe", []string{"web"}},
		{"web", []string{"web-2"}},
		{"dtabase", []string{"database"}},
		{"cahce", []string{"cache"}},
		{"data", []string{"database"}},
		{"xyz", nil},
	} {
		if got := closeNames(tc.name, names); !slices.Equal(got, tc.want) {
			t.Errorf("closeNames(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRunWithCachedImageSaysNothingOfIt(t *testing.T) {
	d := newFakeInstanceDaemon()
	d.cached["alpine:3.21"] = true
	serveInstanceDaemon(t, d)

	out, err := run(t, "run", "-d", "--name", "a", "alpine:3.21")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out, "pulled") || strings.Contains(out, "Resolving") {
		t.Errorf("an image already on the host was reported on:\n%s", out)
	}
	// Nor is its registry asked about it, which may be unreachable, or
	// limiting the host's pulls.
	if slices.Contains(d.calls, "pull alpine:3.21") {
		t.Errorf("calls = %v, want no pull of an image the host holds", d.calls)
	}
}

func TestImagePullReportsUpToDate(t *testing.T) {
	d := newFakeInstanceDaemon()
	serveInstanceDaemon(t, d)

	if out, err := run(t, "pull", "nginx:1.27"); err != nil || !strings.Contains(out, "Image nginx:1.27 pulled in") ||
		!strings.Contains(out, "sha256:0123456789ab,") {
		t.Errorf("first pull = %q, %v", out, err)
	}
	if out, err := run(t, "pull", "nginx:1.27"); err != nil || !strings.Contains(out, "Image nginx:1.27 is up to date") {
		t.Errorf("second pull = %q, %v", out, err)
	}
}

func TestPsWatch(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"ps", "-q", "--watch", "--interval", "200ms"})

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("ps --watch: %v", err)
	}

	// Off a terminal, each redraw follows the last after a blank line.
	frames := strings.Split(strings.TrimSpace(out.String()), "\n\n")
	if len(frames) < 2 {
		t.Fatalf("got %d frames, want at least 2:\n%s", len(frames), out.String())
	}
	for _, f := range frames {
		if f != "cache\ndb\nweb" {
			t.Errorf("frame = %q", f)
		}
	}

	if _, err := run(t, "ps", "--watch", "--interval", "1ms"); err == nil {
		t.Error("an interval that would hammer the daemon should be refused")
	}
}

func TestInfoShowsDefaults(t *testing.T) {
	d := newFakeInstanceDaemon()
	d.host = &dicerdv1.GetHostInfoResponse{Version: "v1", DefaultNetwork: "default"}
	serveInstanceDaemon(t, d)

	out, err := run(t, "info")
	if err != nil {
		t.Fatalf("info: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Defaults: kernel none (name one)\n                 network default\n") {
		t.Errorf("info should say what an instance gets by default:\n%s", out)
	}
}

func TestInstanceStatusForAnInstanceThatEnded(t *testing.T) {
	ago := timestamppb.New(time.Now().Add(-2 * time.Minute))
	soon := timestamppb.New(time.Now().Add(30 * time.Second))
	code := func(c int32) *int32 { return &c }

	tests := []struct {
		inst *dicerdv1.Instance
		want string
	}{
		{&dicerdv1.Instance{State: stateStopped, ExitCode: code(0), FinishTime: ago}, "Exited (0) 2 minutes ago"},
		{
			&dicerdv1.Instance{State: stateFailed, StateError: "exit code 1", ExitCode: code(1), FinishTime: ago},
			"Exited (1) 2 minutes ago",
		},
		{&dicerdv1.Instance{State: stateFailed, StateError: "the guest reset"}, "Failed: the guest reset"},
		{&dicerdv1.Instance{State: stateRestarting, RestartCount: 3, NextRestartTime: soon}, "Restarting (3) in 2"},
		{&dicerdv1.Instance{State: stateRestarting, RestartCount: 1}, "Restarting (1)"},
		{&dicerdv1.Instance{State: stateStopped}, "Stopped"},
	}
	for _, tt := range tests {
		// The wait before a restart is only compared as far as it does not
		// depend on how long the test takes.
		if got := instanceStatus(tt.inst); !strings.HasPrefix(got, tt.want) ||
			(tt.inst.GetNextRestartTime() == nil && got != tt.want) {
			t.Errorf("instanceStatus(%v) = %q, want %q", tt.inst, got, tt.want)
		}
	}
}

func TestParseRestartPolicy(t *testing.T) {
	p, err := parseRestartPolicy("on-failure:5")
	if err != nil || p.GetMode() != dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE || p.GetMaxRetries() != 5 {
		t.Errorf("parseRestartPolicy(on-failure:5) = %v, %v", p, err)
	}
	if _, err := parseRestartPolicy("on-failure:many"); err == nil {
		t.Error("parseRestartPolicy accepted a retry limit that is not a number")
	}
}
