// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import "time"

// Snapshot is a running instance frozen to disk: the guest's memory and
// device state, plus a copy of its overlay disk taken at the same moment.
// Snapshots are stored with their instance and deleted with it.
type Snapshot struct {
	// Name identifies the snapshot within its instance.
	Name string `json:"name"`

	// InstanceID is the instance the snapshot was taken from.
	InstanceID string `json:"instance_id"`

	// InstanceName is looked up when the snapshot is read.
	InstanceName string `json:"-"`

	// HypervisorType and HypervisorVersion took the snapshot, and are used
	// to restore it.
	HypervisorType    HypervisorType `json:"hypervisor_type"`
	HypervisorVersion string         `json:"hypervisor_version"`

	// VCPUs and MemoryBytes are what the guest ran with, and what a restore
	// is admitted on.
	VCPUs       int   `json:"vcpus"`
	MemoryBytes int64 `json:"memory_bytes"`

	// ImageDigest is the image the guest was booted from, and is restored
	// with.
	ImageDigest string `json:"image_digest"`

	CreatedAt time.Time `json:"created_at"`

	// SizeBytes is the space the snapshot occupies, measured when read.
	SizeBytes int64 `json:"-"`
}
