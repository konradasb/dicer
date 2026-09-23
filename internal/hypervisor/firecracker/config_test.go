// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"errors"
	"testing"

	"github.com/dicer-sh/dicer/internal/hypervisor"
)

func testSpec() hypervisor.VirtualMachine {
	return hypervisor.VirtualMachine{
		Boot: hypervisor.BootConfig{
			KernelPath: "/var/lib/dicer/kernels/k/vmlinux",
			KernelArgs: "console=ttyS0",
			InitrdPath: "/var/lib/dicer/initrd/initrd",
		},
		CPU:    hypervisor.CPUConfig{Count: 2},
		Memory: hypervisor.MemoryConfig{SizeBytes: 512 << 20},
		Disks: []hypervisor.DiskConfig{
			{Path: "/rootfs.img", ReadOnly: true},
			{Path: "/overlay.img"},
		},
		NICs: []hypervisor.NetworkInterfaceConfig{
			{TapDevice: "tap-abcd1234", MAC: "02:00:00:00:00:01", MTU: 1500},
		},
		Console: hypervisor.ConsoleConfig{Path: "/run/dicer/serial.log"},
		Vsock:   &hypervisor.VsockConfig{CID: 42, Socket: "/run/dicer/vsock.sock"},
	}
}

func TestNewSetup(t *testing.T) {
	s, err := newSetup(testSpec())
	if err != nil {
		t.Fatalf("newSetup: %v", err)
	}

	if s.boot.KernelImagePath != "/var/lib/dicer/kernels/k/vmlinux" ||
		s.boot.InitrdPath != "/var/lib/dicer/initrd/initrd" ||
		s.boot.BootArgs != "console=ttyS0" {
		t.Errorf("boot source = %+v", s.boot)
	}

	if s.machine.VCPUCount != 2 || s.machine.MemSizeMiB != 512 || s.machine.SMT {
		t.Errorf("machine config = %+v, want 2 vCPUs and 512 MiB without SMT", s.machine)
	}

	// The guest reads its root filesystem from the first disk and its
	// overlay from the second, so their order must survive translation.
	if len(s.drives) != 2 {
		t.Fatalf("got %d drives, want 2", len(s.drives))
	}
	if s.drives[0].DriveID != "disk0" || s.drives[0].PathOnHost != "/rootfs.img" || !s.drives[0].IsReadOnly {
		t.Errorf("first drive = %+v", s.drives[0])
	}
	if s.drives[1].DriveID != "disk1" || s.drives[1].PathOnHost != "/overlay.img" || s.drives[1].IsReadOnly {
		t.Errorf("second drive = %+v", s.drives[1])
	}
	// The guest boots from the initramfs, so nothing is the root device.
	for _, d := range s.drives {
		if d.IsRootDevice {
			t.Errorf("drive %s is marked as the root device", d.DriveID)
		}
	}

	if len(s.nics) != 1 || s.nics[0].IfaceID != "eth0" ||
		s.nics[0].HostDevName != "tap-abcd1234" || s.nics[0].MTU != 1500 {
		t.Errorf("nics = %+v", s.nics)
	}

	if s.vsock == nil || s.vsock.GuestCID != 42 || s.vsock.UDSPath != "/run/dicer/vsock.sock" {
		t.Errorf("vsock = %+v", s.vsock)
	}

	if s.serial == nil || s.serial.SerialOutPath != "/run/dicer/serial.log" {
		t.Errorf("serial = %+v", s.serial)
	}

	if s.hotplug != nil {
		t.Errorf("hotplug = %+v, want none when no hotplug memory is asked for", s.hotplug)
	}
}

func TestNewSetupMemoryRounding(t *testing.T) {
	spec := testSpec()
	// A size that is not a whole number of MiB must round up, never down:
	// a guest asked for 1.5 MiB must not be given 1 MiB.
	spec.Memory.SizeBytes = 3 << 19 // 1.5 MiB
	spec.Memory.HotplugBytes = 200 << 20

	s, err := newSetup(spec)
	if err != nil {
		t.Fatalf("newSetup: %v", err)
	}

	if s.machine.MemSizeMiB != 2 {
		t.Errorf("mem_size_mib = %d, want 2", s.machine.MemSizeMiB)
	}
	// virtio-mem hands out memory in slots, so the region covers whole ones.
	if s.hotplug == nil || s.hotplug.TotalSizeMiB != 256 {
		t.Errorf("hotplug = %+v, want 256 MiB (two %d MiB slots)", s.hotplug, hotplugSlotMiB)
	}
}

func TestNewSetupDiskRateLimit(t *testing.T) {
	spec := testSpec()
	spec.Disks[1].RateLimitBps = 1 << 20
	spec.Disks[1].RateLimitBurstBps = 3 << 20

	s, err := newSetup(spec)
	if err != nil {
		t.Fatalf("newSetup: %v", err)
	}

	limiter := s.drives[1].RateLimiter
	if limiter == nil || limiter.Bandwidth == nil {
		t.Fatalf("rate limiter = %+v, want a bandwidth bucket", limiter)
	}
	if limiter.Bandwidth.Size != 1<<20 || limiter.Bandwidth.RefillTime != 1000 {
		t.Errorf("bucket = %+v, want 1 MiB refilled every second", limiter.Bandwidth)
	}
	if limiter.Bandwidth.OneTimeBurst != 2<<20 {
		t.Errorf("one_time_burst = %d, want the 2 MiB above the rate", limiter.Bandwidth.OneTimeBurst)
	}
	if s.drives[0].RateLimiter != nil {
		t.Error("an unlimited disk got a rate limiter")
	}
}

func TestNewSetupRejectsUnsupported(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*hypervisor.VirtualMachine)
	}{
		{"vcpu hotplug", func(s *hypervisor.VirtualMachine) { s.CPU.MaxCount = 8 }},
		{"cpu topology", func(s *hypervisor.VirtualMachine) {
			s.CPU.Topology = &hypervisor.CPUTopology{Packages: 1}
		}},
		{"cpu affinity", func(s *hypervisor.VirtualMachine) {
			s.CPU.Affinity = []hypervisor.CPUAffinity{{VCPU: 0, HostCPUs: []int{1}}}
		}},
		{"pci passthrough", func(s *hypervisor.VirtualMachine) {
			s.Devices = []hypervisor.PCIDeviceConfig{{Path: "/sys/bus/pci/devices/0000:00:01.0"}}
		}},
		{"gpu", func(s *hypervisor.VirtualMachine) {
			s.GPU = &hypervisor.GPUConfig{Profile: "nvidia-35"}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := testSpec()
			tt.mutate(&spec)

			_, err := newSetup(spec)
			if !errors.Is(err, errors.ErrUnsupported) {
				t.Errorf("newSetup = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestNewSetupRejectsBadSizing(t *testing.T) {
	for _, vcpus := range []int{0, maxVCPUs + 1} {
		spec := testSpec()
		spec.CPU.Count = vcpus
		if _, err := newSetup(spec); err == nil {
			t.Errorf("newSetup with %d vCPUs succeeded", vcpus)
		}
	}

	spec := testSpec()
	spec.Memory.SizeBytes = 0
	if _, err := newSetup(spec); err == nil {
		t.Error("newSetup with no memory succeeded")
	}
}
