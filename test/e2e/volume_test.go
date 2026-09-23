// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestVolumeOutlivesItsInstance writes to a volume from one instance and
// reads it back from another.
//
// That is the promise volumes make -- they are never deleted implicitly, and
// removing an instance leaves its data alone -- and it cannot be checked
// without booting something that can write to the disk.
func TestVolumeOutlivesItsInstance(t *testing.T) {
	var (
		volume = instanceName(t) + "-vol"
		writer = instanceName(t) + "-writer"
		reader = instanceName(t) + "-reader"
		marker = "written-by-" + writer
	)

	env.dicer(t, "volume", "create", volume, "--size", "128MiB")
	t.Cleanup(func() { env.deleteVolume(t, volume) })

	// The first instance formats nothing: the volume arrives as a raw block
	// device, so the guest makes a filesystem on it the way a user would.
	env.createInstance(t, writer, "--mount", "source="+volume+",target=/mnt/data")
	env.startInstance(t, writer)

	env.exec(t, writer, "sh", "-c",
		"mkfs.ext4 -F /dev/vdd >/dev/null 2>&1 || true; "+
			"mount /dev/vdd /mnt/data 2>/dev/null; "+
			"echo "+marker+" > /mnt/data/marker; sync")

	env.dicer(t, "instance", "stop", writer)
	env.waitForState(t, writer, "Stopped")
	env.dicer(t, "instance", "delete", writer, "--force")

	// A second instance, with its own root filesystem, mounting the same
	// volume must see what the first one wrote.
	env.createInstance(t, reader, "--mount", "source="+volume+",target=/mnt/data")
	env.startInstance(t, reader)

	out := env.exec(t, reader, "sh", "-c",
		"mount /dev/vdd /mnt/data 2>/dev/null; cat /mnt/data/marker")
	if got := strings.TrimSpace(out); got != marker {
		t.Errorf("the volume reads back %q, want %q", got, marker)
	}
}

// TestVolumeSurvivesItsInstance is the other half of the promise: a volume is
// never removed implicitly, however the instance using it goes away.
func TestVolumeSurvivesItsInstance(t *testing.T) {
	var (
		volume   = instanceName(t) + "-vol"
		instance = instanceName(t)
	)

	env.dicer(t, "volume", "create", volume, "--size", "128MiB")
	t.Cleanup(func() { env.deleteVolume(t, volume) })

	env.createInstance(t, instance, "--mount", "source="+volume+",target=/mnt/data")
	env.dicer(t, "instance", "delete", instance, "--force")

	if out := env.dicer(t, "volume", "show", volume, "--format", "json"); !strings.Contains(out, volume) {
		t.Errorf("volume %s did not survive its instance:\n%s", volume, out)
	}
}

// deleteVolume removes a volume, logging rather than failing so that it can
// be used for cleanup.
func (e *environment) deleteVolume(t *testing.T, name string) {
	t.Helper()

	ctx, cancel := cleanupContext()
	defer cancel()

	if _, err := e.runDicer(ctx, "volume", "delete", name); err != nil && !isNotFound(err) {
		t.Logf("cleanup: delete volume %s: %v", name, err)
	}
}
