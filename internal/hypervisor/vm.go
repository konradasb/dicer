// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hypervisor

import (
	"errors"
	"fmt"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/humanize"
)

// BootConfig is what the guest boots: a kernel, its command line, and an
// initrd.
type BootConfig struct {
	KernelPath string
	KernelArgs string
	InitrdPath string
}

// CPUConfig describes the guest's virtual CPUs.
type CPUConfig struct {
	Count    int
	MaxCount int
	Topology *CPUTopology
	Affinity []CPUAffinity
}

// CPUTopology describes how vCPUs are presented to the guest as threads,
// cores, dies and packages.
type CPUTopology struct {
	ThreadsPerCore int
	CoresPerDie    int
	DiesPerPackage int
	Packages       int
}

// CPUAffinity pins one vCPU to a set of host CPUs.
type CPUAffinity struct {
	VCPU     int
	HostCPUs []int
}

// MemoryConfig describes the guest's RAM, including any set aside for
// hotplug.
type MemoryConfig struct {
	SizeBytes int64

	// HotplugBytes is how much more memory ResizeVMMemory can give the
	// guest. Zero sets none aside.
	HotplugBytes int64
}

// MemoryResizeStep is the unit a guest's memory is resized in: the block
// virtio-mem plugs and unplugs, the same on both hypervisors.
const MemoryResizeStep = 2 << 20

// CheckMemoryResize returns an error unless a guest that booted with
// bootBytes of memory, and hotplugBytes set aside, can be resized to bytes:
// errors.ErrUnsupported if it booted with none set aside, and
// errdefs.ErrInvalidArgument for a size it cannot have.
func CheckMemoryResize(bytes, bootBytes, hotplugBytes int64) error {
	switch {
	case hotplugBytes <= 0:
		return fmt.Errorf("the guest booted with no memory set aside to resize into: %w", errors.ErrUnsupported)
	case bytes < bootBytes || bytes > bootBytes+hotplugBytes:
		return errdefs.InvalidArgument("memory can be resized from the %s the guest booted with up to %s, not to %s",
			humanize.Bytes(bootBytes), humanize.Bytes(bootBytes+hotplugBytes), humanize.Bytes(bytes))
	case (bytes-bootBytes)%MemoryResizeStep != 0:
		return errdefs.InvalidArgument("memory can be resized only in steps of %s from the %s the guest booted with, not to %s",
			humanize.Bytes(MemoryResizeStep), humanize.Bytes(bootBytes), humanize.Bytes(bytes))
	}
	return nil
}

// VsockConfig describes the vsock device used to reach the in-guest agent.
type VsockConfig struct {
	CID uint32

	// SocketPath is the Unix socket the VMM proxies the device through.
	SocketPath string
}

// PCIDeviceConfig passes a host PCI device through to the guest.
type PCIDeviceConfig struct {
	Path string
}

// ConsoleConfig describes where the guest's serial console is written.
type ConsoleConfig struct {
	// Path is the file the console's output is appended to.
	Path string
}

// DiskConfig attaches a disk image to the guest.
type DiskConfig struct {
	Path     string
	ReadOnly bool

	// RateLimitBytesPerSecond and RateLimitIOPS limit the bytes and the
	// operations per second the disk is read and written at, together. Zero
	// is unlimited.
	RateLimitBytesPerSecond int64
	RateLimitIOPS           int64
}

// NetworkInterfaceConfig attaches the guest to a host TAP device.
type NetworkInterfaceConfig struct {
	TAPDevice string
	MAC       string
	IP        string
	Netmask   string
	MTU       int
}

// GPUConfig describes a mediated GPU device assigned to the guest.
type GPUConfig struct {
	// Profile is the vGPU profile's name, such as nvidia-35.
	Profile string

	// MediatedDeviceUUID is the mediated device's UUID.
	MediatedDeviceUUID string
}

// VMSpec is the full specification of a guest, handed to a Starter to boot.
type VMSpec struct {
	Boot              BootConfig
	CPU               CPUConfig
	Memory            MemoryConfig
	Disks             []DiskConfig
	PCIDevices        []PCIDeviceConfig
	NetworkInterfaces []NetworkInterfaceConfig
	Console           ConsoleConfig
	Vsock             *VsockConfig
	GPU               *GPUConfig
}

// VMInfo is a running guest's observed state, as reported by the VMM.
type VMInfo struct {
	State       VMState `json:"state"`
	MemoryBytes *int64  `json:"memory_bytes,omitempty"`
}

// VMState is the VMM's view of whether a guest is running.
type VMState string

// The states a VMM reports for a guest.
const (
	VMStateStopped VMState = "Stopped"
	VMStateRunning VMState = "Running"
	VMStatePaused  VMState = "Paused"
)
