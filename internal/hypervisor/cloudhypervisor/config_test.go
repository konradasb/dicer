// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"reflect"
	"testing"

	"github.com/konradasb/dicer/internal/hypervisor"
)

// TestVMConfigPassesThroughDevices checks that PCI devices and a GPU's
// mediated device all reach the VMM as VFIO devices, and that a guest with
// none sends no device list.
func TestVMConfigPassesThroughDevices(t *testing.T) {
	tests := []struct {
		name string
		spec hypervisor.VMSpec
		want *[]DeviceConfig
	}{
		{
			name: "none",
		},
		{
			name: "pci devices",
			spec: hypervisor.VMSpec{PCIDevices: []hypervisor.PCIDeviceConfig{
				{Path: "/sys/bus/pci/devices/0000:01:00.0"},
			}},
			want: &[]DeviceConfig{{Path: "/sys/bus/pci/devices/0000:01:00.0"}},
		},
		{
			name: "gpu",
			spec: hypervisor.VMSpec{GPU: &hypervisor.GPUConfig{
				Profile:            "nvidia-35",
				MediatedDeviceUUID: "c2f8e1a4-0d6b-4c3e-9f5a-2b7d8e9f0a1b",
			}},
			want: &[]DeviceConfig{{Path: "/sys/bus/mdev/devices/c2f8e1a4-0d6b-4c3e-9f5a-2b7d8e9f0a1b"}},
		},
		{
			name: "pci devices and gpu",
			spec: hypervisor.VMSpec{
				PCIDevices: []hypervisor.PCIDeviceConfig{{Path: "/sys/bus/pci/devices/0000:01:00.0"}},
				GPU:        &hypervisor.GPUConfig{MediatedDeviceUUID: "c2f8e1a4-0d6b-4c3e-9f5a-2b7d8e9f0a1b"},
			},
			want: &[]DeviceConfig{
				{Path: "/sys/bus/pci/devices/0000:01:00.0"},
				{Path: "/sys/bus/mdev/devices/c2f8e1a4-0d6b-4c3e-9f5a-2b7d8e9f0a1b"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vmConfig(tt.spec).Devices
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Devices = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMemorySetAsideForHotplugIsAligned checks that the memory set aside for
// resizing is rounded up to the 128 MiB Cloud Hypervisor requires, and that
// none is set aside unless asked for.
func TestMemorySetAsideForHotplugIsAligned(t *testing.T) {
	const mib = 1 << 20
	tests := []struct {
		name         string
		hotplugBytes int64
		want         *int64
	}{
		{"none", 0, nil},
		{"aligned", 256 * mib, ptr(int64(256 * mib))},
		{"unaligned", 300 * mib, ptr(int64(384 * mib))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := memoryConfig(hypervisor.MemoryConfig{SizeBytes: 512 * mib, HotplugBytes: tt.hotplugBytes})
			if !reflect.DeepEqual(got.HotplugSize, tt.want) {
				t.Errorf("hotplug_size = %v, want %v", deref(got.HotplugSize), deref(tt.want))
			}
		})
	}
}

// deref returns what p points to, or nil, for a readable failure.
func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
