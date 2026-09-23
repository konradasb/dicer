// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// TestResourcesDefinitionTooBigIsRefused checks that a definition that could
// never start is refused when it is written.
func TestResourcesDefinitionTooBigIsRefused(t *testing.T) {
	name := instanceName(t)

	out, err := env.tryDicer(t,
		"instance", "create", name,
		"--image", testImage, "--kernel", kernelName, "--network", networkName,
		"--vcpus", "1024", "--memory", "512MiB", "--disk", "1GiB")
	if err == nil {
		env.deleteInstance(t, name)
		t.Fatalf("an instance with 1024 vCPUs was defined:\n%s", out)
	}
	if !strings.Contains(out, "more than the host's") {
		t.Errorf("refusal = %q, want it to say why", out)
	}
}

// TestResourcesStartRefusedWhenTheHostIsFull fills the host's memory without
// booting anything big: one instance is defined to take almost all of it,
// a small one is started, and the big one's start must then be refused.
func TestResourcesStartRefusedWhenTheHostIsFull(t *testing.T) {
	big := instanceName(t) + "-big"
	small := instanceName(t) + "-small"

	available := availableMemory(t)

	// Fits on its own, but not beside the small instance's 512MiB.
	env.createInstance(t, big, "--memory", strconv.FormatInt(available-256<<20, 10))
	env.createInstance(t, small)
	env.startInstance(t, small)

	out, err := env.tryDicer(t, "instance", "start", big)
	if err == nil {
		t.Fatalf("a start beyond the host's memory was admitted:\n%s", out)
	}
	if !strings.Contains(out, "this host allows is committed") {
		t.Errorf("refusal = %q, want it to say the host is full", out)
	}

	// A refused start leaves the instance as it was.
	if state := env.instance(t, big).State; state != "Stopped" {
		t.Errorf("state after a refused start = %s, want Stopped", state)
	}
}

// availableMemory reads the memory the host has left for instances.
func availableMemory(t *testing.T) int64 {
	t.Helper()

	// `info --format json` prints the daemon's own record of the host, so
	// the byte counts are JSON numbers rather than the strings protojson
	// would have written.
	var info struct {
		Resources struct {
			Memory struct {
				Available int64 `json:"available"`
			} `json:"memory"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(env.dicer(t, "info", "--format", "json")), &info); err != nil {
		t.Fatalf("parse info: %v", err)
	}

	if info.Resources.Memory.Available <= 0 {
		t.Fatalf("the host reports %d bytes of memory available for instances",
			info.Resources.Memory.Available)
	}

	return info.Resources.Memory.Available
}
