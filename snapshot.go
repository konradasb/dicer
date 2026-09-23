// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import "time"

// Snapshot is a running instance frozen to disk: the guest's memory and
// device state as the hypervisor writes them, plus a copy of the overlay
// disk taken at the same moment.
//
// The disk is part of the snapshot deliberately. A guest resumed from memory
// expects its filesystem exactly as it left it, and an instance that kept
// running after the snapshot has moved on from that; restoring memory over a
// diverged disk corrupts the guest.
//
// Snapshots belong to the instance they were taken from. They live beside
// its spec, so they survive a reboot and are removed with it.
type Snapshot struct {
	// Name identifies the snapshot within its instance.
	Name string `json:"name"`

	// InstanceID is the instance the snapshot was taken from. It is the ID
	// rather than the name so a rename cannot orphan a snapshot.
	InstanceID string `json:"instance_id"`

	// InstanceName is that instance's name, looked up when the snapshot is
	// read rather than stored, for the same reason.
	InstanceName string `json:"-"`

	// HypervisorType and HypervisorVersion are what took the snapshot.
	// Restoring uses the same version: snapshot formats are specific to it.
	HypervisorType    HypervisorType `json:"hypervisor_type"`
	HypervisorVersion string         `json:"hypervisor_version"`

	// VCPUs and MemoryBytes are what the guest ran with when the snapshot
	// was taken. Restoring gives it the same, whatever the spec says now,
	// so that is what a restore is admitted on.
	VCPUs       int   `json:"vcpus"`
	MemoryBytes int64 `json:"memory_bytes"`

	// ImageDigest is the image the guest was booted from. The guest's
	// memory expects that image's root disk, so it is the one restored
	// with, whatever the instance's tag names now.
	ImageDigest string `json:"image_digest"`

	CreatedAt time.Time `json:"created_at"`

	// SizeBytes is what the snapshot occupies on disk. It is measured when
	// the snapshot is read, not recorded, so a sparse copy reports what it
	// actually costs.
	SizeBytes int64 `json:"-"`
}
