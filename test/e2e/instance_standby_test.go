// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
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
