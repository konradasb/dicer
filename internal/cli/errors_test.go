// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer"
)

// runErr runs a command line as the binary does, and returns what it wrote
// to stderr and the exit status.
func runErr(t *testing.T, args ...string) (string, int) {
	t.Helper()

	cmd := NewCommand()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())

	code := exitStatus(cmd, &stderr)
	return stderr.String(), code
}

func TestUsageErrors(t *testing.T) {
	isolateConfig(t)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{
			[]string{"rm"},
			"Error: dicer rm needs at least one instance name\n\n" +
				"Usage:  dicer rm NAME... [flags]\nRun 'dicer rm --help' for more.\n",
		},
		{
			[]string{"ps", "extra"},
			"Error: dicer ps takes no arguments, but got \"extra\"\n\n" +
				"Usage:  dicer ps [flags]\nRun 'dicer ps --help' for more.\n",
		},
		{
			[]string{"logs"},
			"Error: dicer logs needs an instance name\n\n",
		},
		{
			[]string{"logs", "a", "b"},
			"Error: dicer logs got an unexpected argument \"b\"\n\n",
		},
		{
			[]string{"instance", "snapshot", "show", "web"},
			"Error: dicer instance snapshot show needs a snapshot name\n\n",
		},
		{
			[]string{"cp"},
			"Error: dicer cp needs a source and a destination\n\n",
		},
		{
			[]string{"volume", "create", "v1"},
			"Error: dicer volume create needs --size, e.g. --size 10GiB\n\n" +
				"Usage:  dicer volume create NAME [flags]\nRun 'dicer volume create --help' for more.\n",
		},
		{
			[]string{"kernel", "import", "k"},
			"Error: dicer kernel import needs --arch and --url\n\n",
		},
		{
			[]string{"ps", "--bogus"},
			"Error: unknown flag --bogus\n\n",
		},
		{
			[]string{"run", "--vcpus", "lots", "nginx"},
			"Error: invalid --vcpus \"lots\": want a whole number\n\n",
		},
		{
			[]string{"ps", "--timeout", "soon"},
			"Error: invalid --timeout \"soon\": want a duration like 30s, 5m or 1h\n\n",
		},
		{
			[]string{"logs", "-n"},
			"Error: a value is needed for 'n' in -n\n\n",
		},
		{
			[]string{"update"},
			"Error: dicer update needs an instance name\n\n",
		},
		{
			[]string{"create", "a", "b"},
			"Error: dicer create got an unexpected argument \"b\"\n\n",
		},
	} {
		got, code := runErr(t, tc.args...)
		if code != 1 {
			t.Errorf("%v: exit status %d, want 1", tc.args, code)
		}
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("%v wrote:\n%s\nwant it to begin:\n%s", tc.args, got, tc.want)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	isolateConfig(t)

	got, code := runErr(t, "snapshots")
	if code != 1 {
		t.Errorf("exit status %d, want 1", code)
	}
	if want := "Error: dicer has no command \"snapshots\"\nRun 'dicer --help' for a list of commands.\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}

	got, _ = runErr(t, "instance", "strt")
	if want := "Error: dicer instance has no command \"strt\" (did you mean start?)\n" +
		"Run 'dicer instance --help' for a list of commands.\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

func TestRmRunningHintsAtForce(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	got, _ := runErr(t, "rm", "web")
	if want := "Error: rpc error: code = FailedPrecondition desc = instance \"web\" is running: " +
		"stop it first or use -f\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}

	// Not every failure has that fix.
	got, _ = runErr(t, "rm", "nope")
	if strings.Contains(got, "use -f") {
		t.Errorf("a missing instance was told to use -f: %q", got)
	}
}

func TestErrorsAreOneLine(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	got, _ := runErr(t, "start", "wbe")
	if want := "Error: rpc error: code = NotFound desc = no instance \"wbe\" (did you mean web?)\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

func TestUnreachable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{
			status.Error(codes.Unavailable, `connection error: desc = "transport: Error while dialing: `+
				`dial tcp 192.0.2.1:7443: connect: connection refused"`),
			"cannot reach the Dicer daemon at prod (tcp://192.0.2.1:7443): connection refused",
		},
		{
			status.Error(codes.Unavailable, `connection error: desc = "transport: authentication handshake `+
				`failed: remote certificate fingerprint sha256:ab does not match"`),
			"cannot reach the Dicer daemon at prod (tcp://192.0.2.1:7443): the TLS handshake failed: " +
				"remote certificate fingerprint sha256:ab does not match",
		},
		// The daemon's own Unavailable is its message, not a failure to
		// reach it.
		{
			status.Error(codes.Unavailable, "cannot reach docker.io: i/o timeout"),
			"cannot reach docker.io: i/o timeout",
		},
		{status.Error(codes.NotFound, "no instance \"web\""), "no instance \"web\""},
	} {
		got := status.Convert(unreachable("prod (tcp://192.0.2.1:7443)", tc.err)).Message()
		if got != tc.want {
			t.Errorf("unreachable(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}

	if unreachable("x", nil) != nil {
		t.Error("no error should stay no error")
	}
}

func TestNoDaemonIsOneLine(t *testing.T) {
	isolateConfig(t)
	t.Setenv(remoteEnv, "unix:///nonexistent/dicer.sock")

	got, _ := runErr(t, "ps")
	if want := "Error: there is no socket at /nonexistent/dicer.sock; is dicerd running?\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestPullReporterCountsRebuildAsFetched pins a pull that downloads nothing
// but rebuilds the image from cached layers: that is work done, not an
// image that was already there.
func TestPullReporterCountsRebuildAsFetched(t *testing.T) {
	r := newPullReporter(&bytes.Buffer{})
	r.report(dicer.PullProgress{Stage: dicer.StageResolving})
	if r.fetched {
		t.Error("resolving alone counted as fetching")
	}

	r.report(dicer.PullProgress{Stage: dicer.StageUnpacking})
	if !r.fetched {
		t.Error("unpacking did not count as fetching")
	}
}
