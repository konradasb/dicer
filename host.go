// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

// HostInfo describes the daemon and the host it manages.
type HostInfo struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`

	// Hypervisors are the hypervisors this daemon can start instances
	// with, and the versions of each an instance may name.
	Hypervisors []HypervisorInfo `json:"hypervisors"`

	// APIFingerprint is the fingerprint of the certificate the API is
	// served with over TCP, which every remote client pins. Comparing it
	// with a client's remote says whether that client is talking to this
	// daemon. Empty if the API is not served over TCP.
	APIFingerprint string `json:"api_fingerprint,omitempty"`

	// APIAddresses are the addresses enrolment tokens send clients to.
	// Empty if the API is not served over TCP.
	APIAddresses []string `json:"api_addresses,omitempty"`

	// DefaultKernel is the kernel an instance created without one boots
	// with: the one the daemon's configuration names, or else the only
	// kernel there is. Empty if there is no such kernel, and an instance
	// must name one.
	DefaultKernel string `json:"default_kernel,omitempty"`

	// DefaultNetwork is the network an instance created without one
	// attaches to, chosen as DefaultKernel is.
	DefaultNetwork string `json:"default_network,omitempty"`
}

// HypervisorInfo is a hypervisor the daemon carries, and the versions of it
// an instance may name.
type HypervisorInfo struct {
	Type HypervisorType `json:"type"`

	// Versions are those available, the one an instance gets by default
	// first.
	Versions []string `json:"versions"`

	// IsDefault reports whether this is the hypervisor an instance gets
	// when it names none.
	IsDefault bool `json:"is_default"`
}

// HostResources is what the host has, what instances may be given of it,
// and what they hold.
type HostResources struct {
	// CPU is counted in vCPUs, Memory in bytes.
	CPU    ResourceCapacity `json:"cpu"`
	Memory ResourceCapacity `json:"memory"`

	// Disk is the filesystem holding the data directory. It is reported,
	// not enforced.
	Disk HostDisk `json:"disk"`

	// Instances are those holding CPU and memory -- starting, running or
	// paused -- in name order.
	Instances []Holder `json:"instances"`
}

// ResourceCapacity is how much of one resource instances may be given, and
// how much they hold. A start that would take Allocated past Allocatable is
// refused.
type ResourceCapacity struct {
	// Host is what the host has: its logical CPUs, or its memory.
	Host int64 `json:"host"`

	// Reserved is what is kept back from instances for the host itself.
	Reserved int64 `json:"reserved"`

	// Overcommit is how far what remains is stretched: 4 lets four vCPUs
	// share each CPU.
	Overcommit float64 `json:"overcommit"`

	// Allocatable is what instances may be given in total:
	// (Host - Reserved) * Overcommit.
	Allocatable int64 `json:"allocatable"`

	// Allocated is what instances hold, and Available what is left for
	// further ones: Allocatable - Allocated, or zero.
	Allocated int64 `json:"allocated"`
	Available int64 `json:"available"`
}

// HostDisk is how full the filesystem holding the data directory is, and how
// much has been promised on it. Disks are sparse, so ProvisionedBytes may be
// far more than the filesystem's size without anything being wrong -- until
// the guests fill them.
type HostDisk struct {
	// Path is the data directory.
	Path       string `json:"path"`
	TotalBytes int64  `json:"total_bytes"`

	// FreeBytes is what can still be written.
	FreeBytes int64 `json:"free_bytes"`

	// ProvisionedBytes is the sizes of every instance's overlay disk and
	// every volume, added up.
	ProvisionedBytes int64 `json:"provisioned_bytes"`
}
