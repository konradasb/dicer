// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strconv"
	"strings"
	"testing"
)

// TestDaemonRestartReadoptsRunningInstance restarts dicerd underneath a
// running guest.
//
// Virtual machines are separate processes that outlive the daemon, and
// recovery re-adopts them from what is on disk rather than from a journal.
func TestDaemonRestartReadoptsRunningInstance(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	env.startInstance(t, name)

	// Guest uptime is the evidence that the same VM came back. A file would
	// not be: the overlay disk survives a reboot too, so it cannot tell
	// "still running" from "started again".
	before := guestUptime(t, name)

	env.restartDaemon(t)

	readopted := env.instance(t, name)
	if readopted.State != "Running" {
		t.Fatalf("instance is %q after the daemon restarted, want %q", readopted.State, "Running")
	}

	after := guestUptime(t, name)
	if after <= before {
		t.Errorf("guest uptime went from %.2fs to %.2fs: the VM was restarted, not re-adopted",
			before, after)
	}

	// Re-adoption has to leave the daemon able to control the VM again, not
	// merely to report it as running -- and stopping has to wait for a VMM
	// that is not the daemon's child to actually exit.
	pid := env.vmmPID(t, name)
	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")
	if env.processAlive(t, pid) {
		t.Errorf("hypervisor %d is still running after its adopted instance was stopped", pid)
	}
}

// TestDaemonNoticesCrashOfAdoptedHypervisor kills a VMM the daemon adopted
// rather than started.
//
// An adopted VMM is not the daemon's child, so its exit cannot be collected
// with wait(2); the daemon watches it through a pidfd instead. This is the
// only test that exercises that path.
func TestDaemonNoticesCrashOfAdoptedHypervisor(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	env.startInstance(t, name)

	env.restartDaemon(t)
	env.waitForState(t, name, "Running")

	env.killVMM(t, name)

	// A VMM that is not the daemon's child leaves no exit status to read,
	// so all the daemon can say is that it went.
	if reason := env.waitForFailure(t, name); !strings.Contains(reason, "hypervisor exited") {
		t.Errorf("failure reason = %q, want the hypervisor's exit", reason)
	}

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")
}

// TestDaemonReportsHypervisorThatDiedWhileItWasDown kills a VMM while no
// daemon is running, and checks that the next one does not pass the instance
// off as running -- or as cleanly stopped.
func TestDaemonReportsHypervisorThatDiedWhileItWasDown(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	env.startInstance(t, name)

	env.stopDaemon(t)
	pid := env.killVMM(t, name)
	env.startDaemonForTest(t)

	if reason := env.waitForFailure(t, name); !strings.Contains(reason, "not running") {
		t.Errorf("failure reason = %q, want the VMM to have died while dicerd was down", reason)
	}
	if env.processAlive(t, pid) {
		t.Errorf("hypervisor %d is running after being killed", pid)
	}

	env.startInstance(t, name)
	env.exec(t, name, "true")

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")
}

// guestUptime reads how long the guest kernel has been running, in seconds.
func guestUptime(t *testing.T, name string) float64 {
	t.Helper()

	out := env.exec(t, name, "cat", "/proc/uptime")

	// /proc/uptime is "<uptime> <idle>".
	field, _, ok := strings.Cut(strings.TrimSpace(out), " ")
	if !ok {
		t.Fatalf("unexpected /proc/uptime in the guest: %q", out)
	}

	uptime, err := strconv.ParseFloat(field, 64)
	if err != nil {
		t.Fatalf("parse guest uptime %q: %v", field, err)
	}

	return uptime
}
