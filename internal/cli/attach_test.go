// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// runInBackground runs the dicer command line with args, returning what it
// printed and how it ended once it has.
func runInBackground(t *testing.T, args ...string) <-chan runResult {
	t.Helper()

	done := make(chan runResult, 1)
	go func() {
		out, err := run(t, args...)
		done <- runResult{out, err}
	}()
	return done
}

type runResult struct {
	out string
	err error
}

// awaitRun returns what a command run in the background came to.
func awaitRun(t *testing.T, done <-chan runResult) runResult {
	t.Helper()

	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not return")
		return runResult{}
	}
}

// awaitState waits until the fake daemon has an instance in state.
func awaitState(t *testing.T, d *fakeInstanceDaemon, name string, state dicerdv1.InstanceState) {
	t.Helper()

	for range 1000 {
		d.mu.Lock()
		inst, ok := d.instances[name]
		reached := ok && inst.GetState() == state
		d.mu.Unlock()
		if reached {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("instance %s never became %v", name, state)
}

// exitCode is the status err makes the process exit with.
func exitCode(err error) int {
	var exitErr *exitError
	if errors.As(err, &exitErr) {
		return exitErr.code
	}
	if err != nil {
		return 1
	}
	return 0
}

// As docker run does, run without -d writes the console until the instance
// stops, and exits with the status it ended with.
func TestRunAttachedWritesTheConsoleAndExitsWithTheStatus(t *testing.T) {
	d := newFakeInstanceDaemon()
	d.cached["alpine:3.21"] = true
	d.console["job"] = "hello\n"
	serveInstanceDaemon(t, d)

	done := runInBackground(t, "run", "--name", "job", "alpine:3.21", "echo", "hello")
	awaitState(t, d, "job", stateRunning)
	d.stops("job", 3)

	r := awaitRun(t, done)
	if got := exitCode(r.err); got != 3 {
		t.Errorf("exit status = %d (%v), want the instance's 3", got, r.err)
	}
	if !strings.Contains(r.out, "hello\n") {
		t.Errorf("output = %q, want the console", r.out)
	}
	// Defined, then started, so the console is followed from its start.
	if d.created.GetStart() {
		t.Error("the instance was started as it was created, before its console was followed")
	}
	if !slices.Contains(d.calls, "start job") {
		t.Errorf("calls = %q, want the instance started", d.calls)
	}
}

// A job --rm deletes as soon as it ends is still reported on: the status
// comes from its events, which outlive it.
func TestRunAttachedWithRmOnAJobThatEndsAtOnce(t *testing.T) {
	d := newFakeInstanceDaemon()
	d.cached["alpine:3.21"] = true
	d.onStart = func(name string) {
		go d.stopsAndRemoves(name, 4)
	}
	serveInstanceDaemon(t, d)

	r := awaitRun(t, runInBackground(t, "run", "--rm", "--name", "job", "alpine:3.21", "false"))
	if got := exitCode(r.err); got != 4 {
		t.Errorf("exit status = %d (%v), want the job's 4\n%s", got, r.err, r.out)
	}
}

// A job that ends well ends the command well.
func TestRunAttachedSucceedsWithTheJob(t *testing.T) {
	d := newFakeInstanceDaemon()
	d.cached["alpine:3.21"] = true
	d.onStart = func(name string) {
		go d.stops(name, 0)
	}
	serveInstanceDaemon(t, d)

	r := awaitRun(t, runInBackground(t, "run", "--name", "job", "alpine:3.21", "true"))
	if r.err != nil {
		t.Errorf("run: %v\n%s", r.err, r.out)
	}
}
