// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/types"
)

// newDefinitionsDaemon returns a daemon with only what creating the default
// network and kernel needs. Its default network's subnet is one a test host
// is unlikely to be on.
func newDefinitionsDaemon(t *testing.T) *daemon {
	t.Helper()

	dataDir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	definitions, err := filestore.NewManager(filestore.Config{DataDir: dataDir, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	kernels, err := kernel.NewManager(kernel.Config{DataDir: dataDir, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	log, err := events.Open(events.Config{File: filepath.Join(dataDir, eventsFile), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })

	cfg := defaultConfig()
	cfg.DataDir = dataDir
	cfg.Network.DefaultSubnet = "10.250.0.0/16"

	return &daemon{cfg: &cfg, logger: logger, definitions: definitions, kernels: kernels, events: log}
}

func TestDefaultNetworkIsCreatedOnce(t *testing.T) {
	d := newDefinitionsDaemon(t)

	if err := d.ensureDefaultNetwork(); err != nil {
		t.Fatalf("ensureDefaultNetwork: %v", err)
	}
	n, err := d.definitions.Network(types.DefaultNetworkName)
	if err != nil || n.Subnet != "10.250.0.0/16" || n.Gateway != "10.250.0.1" {
		t.Fatalf("default network = %+v, %v; want it on 10.250.0.0/16", n, err)
	}

	// A later start leaves it as it is, even when the subnet configured has
	// changed.
	d.cfg.Network.DefaultSubnet = "10.251.0.0/16"
	if err := d.ensureDefaultNetwork(); err != nil {
		t.Fatalf("ensureDefaultNetwork again: %v", err)
	}
	if again, _ := d.definitions.Network(types.DefaultNetworkName); again.ID != n.ID || again.Subnet != n.Subnet {
		t.Errorf("default network = %+v after a second start, want %+v", again, n)
	}
}

func TestDefaultNetworkRefusesATakenSubnet(t *testing.T) {
	d := newDefinitionsDaemon(t)
	if err := d.definitions.CreateNetwork(types.Network{ID: "n-1", Name: "lan", Subnet: "10.250.1.0/24"}); err != nil {
		t.Fatal(err)
	}

	if err := d.ensureDefaultNetwork(); !errors.Is(err, errdefs.ErrExists) {
		t.Errorf("ensureDefaultNetwork = %v, want the overlap refused", err)
	}
}

// defaultKernelBinary returns the path of the default kernel's binary,
// and what it holds.
func defaultKernelBinary(t *testing.T, d *daemon) (string, []byte) {
	t.Helper()

	k, err := d.definitions.Kernel(types.DefaultKernelName)
	if err != nil {
		t.Fatalf("the default kernel is not defined: %v", err)
	}
	path := filepath.Join(d.cfg.DataDir, "kernels", k.ID, "vmlinux")
	data, _ := os.ReadFile(path)
	return path, data
}

// TestDefaultKernelIsExtractedBeforeItIsDefined checks that the default
// kernel is on the host once it is defined, and is the one this binary
// carries.
func TestDefaultKernelIsExtractedBeforeItIsDefined(t *testing.T) {
	d := newDefinitionsDaemon(t)

	if err := d.ensureDefaultKernel(); err != nil {
		t.Fatalf("ensureDefaultKernel: %v", err)
	}
	k, err := d.definitions.Kernel(types.DefaultKernelName)
	if err != nil || k.SHA256 != kernel.Default().SHA256 {
		t.Fatalf("default kernel = %+v, %v; want the one this binary carries", k, err)
	}
	if _, err := d.kernels.Path(k); err != nil {
		t.Errorf("the default kernel is not on the host: %v", err)
	}
}

// TestDefaultKernelIsExtractedAgainIfItsCopyIsGoneOrDamaged checks that a
// default kernel whose copy has gone from the host, or been changed, is
// extracted again at start, under the same ID.
func TestDefaultKernelIsExtractedAgainIfItsCopyIsGoneOrDamaged(t *testing.T) {
	for _, tt := range []struct {
		name   string
		damage func(path string) error
	}{
		{"gone", os.Remove},
		{"damaged", func(path string) error { return os.WriteFile(path, []byte("damaged"), 0o755) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := newDefinitionsDaemon(t)
			if err := d.ensureDefaultKernel(); err != nil {
				t.Fatal(err)
			}
			path, _ := defaultKernelBinary(t, d)
			if err := tt.damage(path); err != nil {
				t.Fatal(err)
			}

			if err := d.ensureDefaultKernel(); err != nil {
				t.Fatal(err)
			}
			k, _ := d.definitions.Kernel(types.DefaultKernelName)
			if again, _ := defaultKernelBinary(t, d); again != path {
				t.Errorf("the default kernel moved from %s to %s", path, again)
			}
			if _, err := d.kernels.Path(k); err != nil {
				t.Errorf("the default kernel is not on the host again: %v", err)
			}
		})
	}
}

// TestDefaultKernelAnOlderVersionCarriedIsReplaced checks that the default
// kernel an older version of Dicer carried, with another SHA-256, is
// replaced in place by the one this binary carries.
func TestDefaultKernelAnOlderVersionCarriedIsReplaced(t *testing.T) {
	d := newDefinitionsDaemon(t)
	if err := d.ensureDefaultKernel(); err != nil {
		t.Fatal(err)
	}
	k, _ := d.definitions.Kernel(types.DefaultKernelName)
	path, _ := defaultKernelBinary(t, d)

	// What an older version would have left.
	k.SHA256 = strings.Repeat("ab", 32)
	if err := d.definitions.UpdateKernel(k); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("an older kernel"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := d.ensureDefaultKernel(); err != nil {
		t.Fatalf("ensureDefaultKernel after an upgrade: %v", err)
	}
	updated, _ := d.definitions.Kernel(types.DefaultKernelName)
	if updated.ID != k.ID || updated.SHA256 != kernel.Default().SHA256 {
		t.Errorf("default kernel = %+v, want the one this binary carries under the same ID", updated)
	}
	if _, err := d.kernels.Path(updated); err != nil {
		t.Errorf("the new default kernel is not on the host: %v", err)
	}
}
