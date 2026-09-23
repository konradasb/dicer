// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dicer-sh/dicer/internal/hypervisor"
)

// TestAgainstRealFirecracker drives the embedded binary itself: it extracts
// it, reads its version, launches it and configures a guest over its API.
//
// Everything short of booting works without /dev/kvm, so this covers the
// parts that no fake can vouch for -- that the embedded binary runs, that
// its version is the one claimed, and that Firecracker accepts the requests
// this package builds. Booting itself needs KVM and is exercised by hand.
func TestAgainstRealFirecracker(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker runs on Linux only")
	}

	dir := shortTempDir(t)
	binaryPath := filepath.Join(dir, "firecracker")
	if _, err := Extract(binaryPath, DefaultVersion); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	starter, err := NewStarter(binaryPath)
	if err != nil {
		t.Fatalf("NewStarter: %v", err)
	}
	if starter.Version() != string(DefaultVersion) {
		t.Errorf("version = %q, want %q", starter.Version(), DefaultVersion)
	}

	socketPath := filepath.Join(dir, "api.sock")
	vmm, hv, cu, err := starter.start(t.Context(), socketPath, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cu.Clean()
	t.Cleanup(vmm.Terminate)

	info, err := hv.GetVMInfo(t.Context())
	if err != nil {
		t.Fatalf("GetVMInfo: %v", err)
	}
	if info.State != "Stopped" {
		t.Errorf("state = %q, want Stopped before the guest is booted", info.State)
	}

	// Firecracker checks the paths it is given, so the guest is configured
	// with real files.
	spec := testSpec()
	spec.Boot.KernelPath = touch(t, dir, "vmlinux")
	spec.Boot.InitrdPath = touch(t, dir, "initrd")
	spec.Disks = []hypervisor.DiskConfig{{Path: touch(t, dir, "rootfs.img"), ReadOnly: true}}
	spec.NICs = nil // a TAP device would have to exist on the host
	spec.Vsock = nil
	spec.Console.Path = filepath.Join(dir, "serial.log")

	setup, err := newSetup(spec)
	if err != nil {
		t.Fatalf("newSetup: %v", err)
	}
	if err := setup.apply(t.Context(), hv.client); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Firecracker's own view of the guest must match what it was told.
	var cfg vmConfig
	if err := hv.client.get(t.Context(), "/vm/config", &cfg); err != nil {
		t.Fatalf("get vm config: %v", err)
	}
	if cfg.MachineConfig.VCPUCount != spec.CPU.Count {
		t.Errorf("vcpu_count = %d, want %d", cfg.MachineConfig.VCPUCount, spec.CPU.Count)
	}
	if want := ceilDiv(spec.Memory.SizeBytes, mib); cfg.MachineConfig.MemSizeMiB != want {
		t.Errorf("mem_size_mib = %d, want %d", cfg.MachineConfig.MemSizeMiB, want)
	}

	// A guest that has not booted cannot be paused, and Firecracker says so.
	if err := hv.PauseVM(t.Context()); err == nil {
		t.Error("PauseVM succeeded before the guest booted")
	}
}

// touch creates an empty file and returns its path.
func touch(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}
