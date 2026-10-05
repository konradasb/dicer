// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import "time"

// SnapshotKind is what a Snapshot holds.
type SnapshotKind string

const (
	// SnapshotKindMemory is a running or paused guest frozen to disk: its
	// memory and device state, with its overlay disk as it was at the same
	// moment. Restoring it resumes the guest where it was.
	SnapshotKindMemory SnapshotKind = "memory"

	// SnapshotKindDisk is a stopped instance's overlay disk alone. Restoring
	// it rolls the disk back, and the guest boots from it afresh.
	SnapshotKindDisk SnapshotKind = "disk"
)

// Snapshot is an instance frozen to disk. It is a resource of its own: it
// outlives the instance it was taken from, which may since have been
// renamed, changed or deleted. It never holds the instance's volumes.
type Snapshot struct {
	ID   string       `yaml:"id" json:"id"`
	Name string       `yaml:"name" json:"name"`
	Kind SnapshotKind `yaml:"kind" json:"kind"`

	// Instance is the definition of the instance the snapshot was taken
	// from, as it was then.
	Instance InstanceSpec `yaml:"instance" json:"instance"`

	// IP and MAC are the guest's address on its network, which a memory
	// snapshot's guest keeps. A disk snapshot has neither.
	IP  string `yaml:"ip,omitempty" json:"ip,omitempty"`
	MAC string `yaml:"mac,omitempty" json:"mac,omitempty"`

	// HypervisorType and HypervisorVersion took a memory snapshot, and are
	// the only ones that can restore it. A disk snapshot has neither.
	HypervisorType    HypervisorType `yaml:"hypervisor_type,omitempty" json:"hypervisor_type,omitempty"`
	HypervisorVersion string         `yaml:"hypervisor_version,omitempty" json:"hypervisor_version,omitempty"`

	// VCPUs, MemoryBytes and ImageDigest are what a memory snapshot's guest
	// ran with, which a restore is admitted on and boots the image of. They
	// can differ from Instance's after a resize or an image update.
	VCPUs       int    `yaml:"vcpus,omitempty" json:"vcpus,omitempty"`
	MemoryBytes int64  `yaml:"memory_bytes,omitempty" json:"memory_bytes,omitempty"`
	ImageDigest string `yaml:"image_digest,omitempty" json:"image_digest,omitempty"`

	CreatedAt time.Time `yaml:"created_at" json:"created_at"`

	// SizeBytes is the space the snapshot occupies, measured when read.
	SizeBytes int64 `yaml:"-" json:"-"`
}
