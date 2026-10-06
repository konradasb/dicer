// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestInstanceStandbyResumesWhereItWas checks that standby ends an
// instance's hypervisor, keeps the instance on standby across a daemon
// restart, and that a start brings back what only its memory held, at the
// address it had.
func TestInstanceStandbyResumesWhereItWas(t *testing.T) {
	for _, hypervisor := range []string{"cloud-hypervisor", "firecracker"} {
		t.Run(hypervisor, func(t *testing.T) {
			name := instanceName(t)
			const marker = "only-in-memory"

			env.createInstance(t, name, "--hypervisor-type", hypervisor)
			before := env.startInstance(t, name)
			env.exec(t, name, "sh", "-c",
				"mkdir -p /mnt/mem && mount -t tmpfs none /mnt/mem && echo "+marker+" > /mnt/mem/marker")
			pid := env.vmmPID(t, name)

			env.dicer(t, "standby", name)

			if state := env.instance(t, name).State; state != "Standby" {
				t.Fatalf("instance is %q after standby, want Standby", state)
			}
			if env.processAlive(t, pid) {
				t.Errorf("the hypervisor, PID %d, still runs on standby", pid)
			}

			env.restartDaemon(t)
			if state := env.instance(t, name).State; state != "Standby" {
				t.Errorf("instance is %q after a daemon restart, want Standby", state)
			}

			after := env.startInstance(t, name)
			if after.IP != before.IP {
				t.Errorf("resumed at %s, want the address it had, %s", after.IP, before.IP)
			}
			if got := strings.TrimSpace(env.exec(t, name, "cat", "/mnt/mem/marker")); got != marker {
				t.Errorf("the resumed guest reads %q from its tmpfs, want %q: memory was not kept", got, marker)
			}
		})
	}
}

// TestInstanceStopDiscardsStandby checks that stopping an instance on
// standby makes its next start a fresh boot.
func TestInstanceStopDiscardsStandby(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	env.startInstance(t, name)
	env.exec(t, name, "sh", "-c", "mkdir -p /mnt/mem && mount -t tmpfs none /mnt/mem && echo x > /mnt/mem/marker")

	env.dicer(t, "standby", name)
	env.dicer(t, "stop", name)
	if state := env.instance(t, name).State; state != "Stopped" {
		t.Fatalf("instance is %q after stopping it on standby, want Stopped", state)
	}

	env.startInstance(t, name)
	if _, err := env.tryExec(t, name, "cat", "/mnt/mem/marker"); err == nil {
		t.Error("a start after a stop resumed what standby froze, rather than booting afresh")
	}
}

// TestIdleInstanceGoesOnStandby checks that an instance given standby_after
// is put on standby once it has been idle that long, while one beside it
// that keeps a vCPU busy is left running.
func TestIdleInstanceGoesOnStandby(t *testing.T) {
	var (
		idle = instanceName(t) + "-idle"
		busy = instanceName(t) + "-busy"
	)

	env.createInstance(t, idle, "--standby-after", "1m")
	env.createInstance(t, busy, "--standby-after", "1m", "--", "sh", "-c", "while :; do :; done")
	env.startInstance(t, idle)
	env.startInstance(t, busy)

	// Judged a minute at a time: a minute of samples to be idle for, and a
	// minute more to catch it.
	deadline := time.Now().Add(4 * time.Minute)
	for env.instance(t, idle).State != "Standby" {
		if time.Now().After(deadline) {
			t.Fatalf("the idle instance is %q after 4m, want Standby", env.instance(t, idle).State)
		}
		time.Sleep(5 * time.Second)
	}

	if state := env.instance(t, busy).State; state != "Running" {
		t.Errorf("the busy instance is %q, want Running", state)
	}
}

// TestConnectionWakesInstanceOnStandby checks that an instance that goes on
// standby by itself is woken by a connection to a port it publishes, which
// reaches its guest, as do the connections after it.
func TestConnectionWakesInstanceOnStandby(t *testing.T) {
	name := instanceName(t)
	const port = "18080"

	// Alpine's busybox has no httpd: nc answers each request with a page,
	// once it has read the request, so as not to reset the connection.
	env.createInstance(t, name, "--standby-after", "15m", "-p", port+":80", "--",
		"nc", "-lk", "-p", "80", "-e", "sh", "-c",
		`while read -r l && [ -n "$(echo "$l" | tr -d "\r")" ]; do :; done; printf "HTTP/1.0 200 OK\r\n\r\nhello\n"`)
	env.startInstance(t, name)

	// The host's own address: a published port is not reached from its
	// loopback.
	url := "http://$(hostname -I | cut -d' ' -f1):" + port + "/"
	get := func() (string, error) {
		ctx, cancel := commandContext(t)
		defer cancel()
		return env.host.runShell(ctx, "curl -sf --max-time 30 "+url)
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(time.Second) {
		if _, err := get(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the guest's web server did not answer on its published port")
		}
	}

	env.dicer(t, "standby", name)
	if state := env.instance(t, name).State; state != "Standby" {
		t.Fatalf("instance is %q, want Standby", state)
	}

	for i, what := range []string{"the connection that wakes it", "a connection after it"} {
		out, err := get()
		if err != nil || strings.TrimSpace(out) != "hello" {
			t.Errorf("%s got %q (%v), want the guest's page", what, out, err)
		}
		if state := env.instance(t, name).State; i == 0 && state != "Running" {
			t.Errorf("instance is %q after a connection woke it, want Running", state)
		}
	}
}
