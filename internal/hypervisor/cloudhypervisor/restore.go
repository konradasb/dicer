// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/konradasb/dicer/internal/atomicfile"
)

// snapshotConfigFile is the file of a snapshot holding the VM's
// configuration, as Cloud Hypervisor writes it.
const snapshotConfigFile = "config.json"

// stageRestore prepares in dir a snapshot to restore from that is the one in
// snapshotPath with its network interfaces connected to tap. Cloud Hypervisor
// takes the TAP device from the snapshot's configuration and has no way to
// override it, so the configuration is rewritten; every other file is
// linked to the snapshot's.
func stageRestore(snapshotPath, dir, tap string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	entries, err := os.ReadDir(snapshotPath)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == snapshotConfigFile {
			continue
		}
		if err := os.Symlink(filepath.Join(snapshotPath, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}

	data, err := os.ReadFile(filepath.Join(snapshotPath, snapshotConfigFile))
	if err != nil {
		return err
	}
	// A map, not VmConfig: a newer Cloud Hypervisor writes fields the
	// client does not know, which must survive.
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse %s: %w", snapshotConfigFile, err)
	}
	networkInterfaces, _ := config["net"].([]any)
	if len(networkInterfaces) == 0 {
		return errors.New("the snapshot's VM has no network interface")
	}
	for _, n := range networkInterfaces {
		networkInterface, ok := n.(map[string]any)
		if !ok {
			return fmt.Errorf("parse %s: a network interface is not an object", snapshotConfigFile)
		}
		networkInterface["tap"] = tap
	}

	if data, err = json.Marshal(config); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, snapshotConfigFile), data, 0o600)
}
