// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStageRestoreConnectsAnotherTAP(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if err := os.MkdirAll(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"net":[{"tap":"dicer-source","mac":"02:00:00:00:00:01"}],"from_a_newer_version":7}`
	for name, content := range map[string]string{"config.json": config, "state.json": "{}", "memory-ranges": "memory"} {
		if err := os.WriteFile(filepath.Join(snapshot, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	staged := filepath.Join(t.TempDir(), "restore")
	if err := stageRestore(snapshot, staged, "dicer-fork"); err != nil {
		t.Fatalf("stageRestore: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(staged, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Net []struct {
			TAP string `json:"tap"`
			MAC string `json:"mac"`
		} `json:"net"`
		FromANewerVersion int `json:"from_a_newer_version"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Net) != 1 || got.Net[0].TAP != "dicer-fork" || got.Net[0].MAC != "02:00:00:00:00:01" {
		t.Errorf("staged network interfaces = %+v, want the snapshot's on dicer-fork", got.Net)
	}
	if got.FromANewerVersion != 7 {
		t.Error("a field the client does not know was lost")
	}

	for _, name := range []string{"state.json", "memory-ranges"} {
		if target, err := os.Readlink(filepath.Join(staged, name)); err != nil || target != filepath.Join(snapshot, name) {
			t.Errorf("%s is staged as %q (%v), want a link to the snapshot's", name, target, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(snapshot, "config.json")); string(data) != config {
		t.Error("the snapshot itself was changed")
	}
}
