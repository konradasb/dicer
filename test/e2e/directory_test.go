// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestDirectoryMountIsSharedLive shares a host directory with a guest, and
// checks that each side sees what the other writes, while it runs.
//
// Only a booted guest can show it: virtiofsd on the host, Cloud Hypervisor's
// virtio-fs device and shared memory, the guest kernel's virtiofs, and
// dicer-init's mount all have to meet.
func TestDirectoryMountIsSharedLive(t *testing.T) {
	env.needVirtiofsd(t)

	var (
		name   = instanceName(t)
		shared = env.paths.root + "/shared/" + name
		docs   = env.paths.root + "/docs/" + name
	)
	env.hostShell(t, "mkdir -p "+shared+" "+docs+" && echo from-the-host > "+shared+"/hello && echo manual > "+docs+"/readme")

	env.createInstance(t, name,
		"--mount", "type=directory,source="+shared+",target=/app",
		"--mount", "type=directory,source="+docs+",target=/docs,readonly")
	env.startInstance(t, name)

	if got := strings.TrimSpace(env.exec(t, name, "cat", "/app/hello")); got != "from-the-host" {
		t.Errorf("the guest reads %q from the shared directory, want what the host wrote", got)
	}

	// The guest's writes reach the host at once, and the host's the guest.
	env.exec(t, name, "sh", "-c", "echo from-the-guest > /app/reply")
	if got := strings.TrimSpace(env.hostShell(t, "cat "+shared+"/reply")); got != "from-the-guest" {
		t.Errorf("the host reads %q, want what the guest wrote", got)
	}
	env.hostShell(t, "echo changed > "+shared+"/hello")
	if got := strings.TrimSpace(env.exec(t, name, "cat", "/app/hello")); got != "changed" {
		t.Errorf("the guest reads %q after the host changed it, want the change", got)
	}

	// A read-only share can be read and not written.
	if got := strings.TrimSpace(env.exec(t, name, "cat", "/docs/readme")); got != "manual" {
		t.Errorf("the guest reads %q from the read-only share", got)
	}
	if _, err := env.tryExec(t, name, "sh", "-c", "echo x > /docs/new"); err == nil {
		t.Error("the guest wrote to a read-only share")
	}

	// It cannot be snapshotted.
	if _, err := env.tryDicer(t, "instance", "snapshot", "create", name); err == nil ||
		!strings.Contains(err.Error(), "mounts a host directory") {
		t.Errorf("snapshot of an instance with a shared directory = %v, want it refused", err)
	}
}

// needVirtiofsd skips the test on a host without a virtiofsd that has
// --readonly: directory mounts need virtiofsd, which Dicer does not carry.
func (e *environment) needVirtiofsd(t *testing.T) {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	// The test shares a directory read-only, which needs a virtiofsd that
	// has --readonly.
	_, err := e.host.runShell(ctx, `v=$(command -v virtiofsd || ls /usr/libexec/virtiofsd /usr/lib/virtiofsd 2>/dev/null | head -n 1) && `+
		`test -n "$v" && "$v" --help | grep -q -- --readonly`)
	if err != nil {
		t.Skip("the host has no virtiofsd with --readonly; install one to test directory mounts")
	}
}

// hostShell runs a shell snippet on the host, failing the test if it fails.
func (e *environment) hostShell(t *testing.T, script string) string {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	out, err := e.host.runShell(ctx, script)
	if err != nil {
		t.Fatalf("%s: %v", script, err)
	}
	return out
}
