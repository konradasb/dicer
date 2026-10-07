// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// TestWarnDeprecatedHypervisorVersionsNamesWhatMustMove covers the warnings the
// daemon logs as it starts: one for each instance that names a deprecated
// version, each guest running on one and each memory snapshot taken with one,
// and none for what the version's removal leaves alone.
func TestWarnDeprecatedHypervisorVersionsNamesWhatMustMove(t *testing.T) {
	h := newHarness(t)
	// A newer default makes the harness's version deprecated.
	h.manager.starters[types.HypervisorTypeCloudHypervisor] = []hypervisor.Starter{
		&fakeStarter{version: "v53.0.0"}, h.starter,
	}
	var logs bytes.Buffer
	h.manager.logger = slog.New(slog.NewTextHandler(&logs, nil))

	pinned := seedInstance(t, h.definitions, "pinned")
	pinned.HypervisorVersion = testHypervisorVersion
	h.definitions.instances[pinned.Name] = pinned
	seedInstance(t, h.definitions, "fresh")
	h.running(t)
	h.definitions.snapshots["old"] = types.Snapshot{
		Name: "old", Kind: types.SnapshotKindMemory,
		HypervisorType: types.HypervisorTypeCloudHypervisor, HypervisorVersion: testHypervisorVersion,
	}
	h.definitions.snapshots["disk"] = types.Snapshot{
		Name: "disk", Kind: types.SnapshotKindDisk,
		HypervisorType: types.HypervisorTypeCloudHypervisor, HypervisorVersion: testHypervisorVersion,
	}

	h.manager.WarnDeprecatedHypervisorVersions(t.Context())

	got := logs.String()
	for _, want := range []string{
		`msg="instance names a deprecated hypervisor version" instance=pinned`,
		`msg="instance's guest runs on a deprecated hypervisor version" instance=web`,
		`msg="memory snapshot was taken with a deprecated hypervisor version" snapshot=old`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("logs lack %s:\n%s", want, got)
		}
	}
	// An instance that names no version moves to the default as it boots,
	// and a disk snapshot needs no hypervisor.
	for _, unwanted := range []string{"instance=fresh", "snapshot=disk"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("logs warn about %s:\n%s", unwanted, got)
		}
	}
}

// TestBootWarnsOfDeprecatedHypervisorVersion covers the warning the daemon logs
// when it boots an instance on a deprecated version.
func TestBootWarnsOfDeprecatedHypervisorVersion(t *testing.T) {
	h := newHarness(t)
	h.manager.starters[types.HypervisorTypeCloudHypervisor] = []hypervisor.Starter{
		&fakeStarter{version: "v53.0.0"}, h.starter,
	}
	var logs bytes.Buffer
	h.manager.logger = slog.New(slog.NewTextHandler(&logs, nil))

	h.instance.HypervisorVersion = testHypervisorVersion
	h.definitions.instances[h.instance.Name] = h.instance
	if err := h.manager.Start(t.Context(), h.instance); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if want := `msg="booting an instance on a deprecated hypervisor version" instance=web`; !strings.Contains(logs.String(), want) {
		t.Errorf("logs lack %s:\n%s", want, logs.String())
	}
}
