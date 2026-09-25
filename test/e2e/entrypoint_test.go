// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestEntrypointSeesItsOwnProcesses checks that an entrypoint in exec mode has
// a /proc of its own PID namespace: that the process /proc calls 1 is the
// entrypoint, not dicer-init. With the machine's /proc instead, anything reading
// /proc/<pid> -- ps, or containerd looking up its parent -- finds the wrong
// process or none.
func TestEntrypointSeesItsOwnProcesses(t *testing.T) {
	name := instanceName(t)
	env.createInstance(t, name, "--",
		"sh", "-c", `echo "pid1=$(tr '\0' ' ' </proc/1/cmdline)"; exec sleep 3600`)
	env.startInstance(t, name)

	var line string
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline) && line == ""; {
		for l := range strings.SplitSeq(env.dicer(t, "instance", "logs", name), "\n") {
			if strings.HasPrefix(l, "pid1=") {
				line = l
			}
		}
		time.Sleep(time.Second)
	}

	if !strings.HasPrefix(line, "pid1=sh -c") {
		t.Errorf("the entrypoint's /proc/1 is %q, want the entrypoint itself, sh -c", line)
	}
}

// TestEntrypointThatCannotStart checks that an entrypoint the image does not
// have ends the instance, as a shell would, with 127: not an instance that
// runs with nothing in it.
func TestEntrypointThatCannotStart(t *testing.T) {
	name := instanceName(t)
	env.createInstance(t, name, "--", "/no/such/command")
	env.dicer(t, "instance", "start", name)

	reason := env.waitForFailure(t, name)
	if got := env.instance(t, name).ExitCode; got == nil || *got != 127 {
		t.Errorf("exit code = %v, want 127 (%s)", got, reason)
	}
}

// TestEntrypointRunsDocker runs Docker inside a guest, as a CI runner would:
// dockerd needs the kernel's netfilter for its bridge network, and containerd
// needs a /proc that matches its PID namespace.
func TestEntrypointRunsDocker(t *testing.T) {
	name := instanceName(t)
	env.createInstance(t, name,
		"--image", "docker.io/library/docker:27-dind",
		"--vcpus", "2", "--memory", "2GiB", "--disk", "8GiB")
	env.startInstance(t, name)

	var err error
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if _, err = env.tryExec(t, name, "docker", "info"); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("dockerd never answered: %v", err)
	}

	// A container that reaches the network proves the bridge and its NAT
	// rules, not only that a container can start.
	env.exec(t, name, "docker", "run", "--rm", testImage,
		"wget", "-q", "-O", "/dev/null", "-T", "10", "http://example.com")
}
