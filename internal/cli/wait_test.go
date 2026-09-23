// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"strings"
	"testing"
	"time"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// stoppedInstance is an instance that has already ended with a status.
func stoppedInstance(name string, exitCode int32) *dicerdv1.Instance {
	return &dicerdv1.Instance{
		Name: name, ImageRef: "alpine:3.21", State: stateStopped, ExitCode: &exitCode,
	}
}

// An instance that has already stopped is not waited for: its last status is
// the answer, and it is there to be read.
func TestWaitOnAStoppedInstance(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(stoppedInstance("job", 0)))

	out, err := run(t, "wait", "job")
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "0" {
		t.Errorf("wait printed %q, want the exit status", out)
	}
}

// A running instance is waited on, and the stop is noticed as it is
// reported rather than by asking again and again.
func TestWaitWaitsForARunningInstance(t *testing.T) {
	d := newFakeInstanceDaemon(&dicerdv1.Instance{Name: "job", ImageRef: "alpine:3.21", State: stateRunning})
	serveInstanceDaemon(t, d)

	done := make(chan string, 1)
	go func() {
		// run reports what the command printed; the status it exits with is
		// what TestWaitExitsWithTheInstanceStatus covers.
		out, _ := run(t, "wait", "job")
		done <- out
	}()

	// Give the wait time to subscribe before anything happens, so the test
	// covers the waiting rather than the already-stopped path.
	time.Sleep(50 * time.Millisecond)
	d.stops("job", 7)

	select {
	case out := <-done:
		if !strings.Contains(out, "7") {
			t.Errorf("wait printed %q, want the status it ended with", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return after the instance stopped")
	}
}

// An instance deleted as it stopped -- which --rm asks for -- cannot be asked
// for its status afterwards, so the status its last event reported is the
// answer.
func TestWaitOnAnInstanceRemovedWhenItStopped(t *testing.T) {
	d := newFakeInstanceDaemon(&dicerdv1.Instance{Name: "job", ImageRef: "alpine:3.21", State: stateRunning})
	serveInstanceDaemon(t, d)

	done := make(chan string, 1)
	go func() {
		out, _ := run(t, "wait", "job")
		done <- out
	}()

	time.Sleep(50 * time.Millisecond)
	d.stopsAndRemoves("job", 4)

	select {
	case out := <-done:
		if strings.TrimSpace(out) != "4" {
			t.Errorf("wait printed %q, want the status its last event reported", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return after the instance was removed")
	}
}
