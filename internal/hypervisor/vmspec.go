// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hypervisor

// BootConfig is what the guest boots: a kernel, its command line, and an initramfs.
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

// CPUTopology describes how vCPUs are presented to the guest as threads, cores, dies and packages.
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

// MemoryConfig describes the guest's RAM, including any set aside for hotplug.
type MemoryConfig struct {
	SizeBytes    int64
	HotplugBytes int64
}

// VsockConfig describes the vsock device used to reach the in-guest agent.
type VsockConfig struct {
	CID    uint32
	Socket string
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

// DiskConfig attaches a disk image to the guest, optionally rate-limited.
type DiskConfig struct {
	Path              string
	ReadOnly          bool
	RateLimitBurstBps int64
	RateLimitBps      int64
}

// NetworkInterfaceConfig attaches the guest to a host TAP device.
type NetworkInterfaceConfig struct {
	TapDevice string
	MAC       string
	IP        string
	Netmask   string
	MTU       int
}

// GPUConfig describes a mediated GPU device assigned to the guest.
type GPUConfig struct {
	Profile  string // vGPU profile name (e.g. "nvidia-35")
	MdevUUID string // mediated device UUID
}

// VirtualMachine is the full specification of a guest, handed to a Starter to boot.
type VirtualMachine struct {
	Boot    BootConfig
	CPU     CPUConfig
	Memory  MemoryConfig
	Disks   []DiskConfig
	Devices []PCIDeviceConfig
	NICs    []NetworkInterfaceConfig
	Console ConsoleConfig
	Vsock   *VsockConfig
	GPU     *GPUConfig
}

// VirtualMachineInfo is a running guest's observed state, as reported by the VMM.
type VirtualMachineInfo struct {
	State       VirtualMachineState `json:"state"`
	MemoryBytes *int64              `json:"memory_bytes,omitempty"`
}

// VirtualMachineState is the VMM's view of whether a guest is running.
type VirtualMachineState string

// The states a VMM reports for a guest.
const (
	VirtualMachineStateStopped VirtualMachineState = "Stopped"
	VirtualMachineStateRunning VirtualMachineState = "Running"
	VirtualMachineStatePaused  VirtualMachineState = "Paused"
)
