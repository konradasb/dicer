// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import "time"

// Image is a container image pulled and converted into a disk a guest boots
// from.
type Image struct {
	Name       string            `json:"name"`
	Digest     string            `json:"digest"`
	DiskPath   string            `json:"disk_path"`
	SizeBytes  int64             `json:"size_bytes"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Cmd        []string          `json:"cmd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`

	// HealthCheck is the check the image declares, used by an instance that
	// sets none of its own. Nil if it declares none.
	HealthCheck *HealthCheck `json:"health_check,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// LastUsedAt is when the image was last pulled or in use: by an
	// instance defined to boot from it, a running guest or a snapshot. It is
	// what garbage collection judges an image by.
	LastUsedAt time.Time `json:"last_used_at,omitzero"`
}

// PullPolicy says when creating an instance pulls its image, as docker run
// --pull does. The zero value behaves as PullMissing.
type PullPolicy string

const (
	// PullMissing pulls the image only if the host does not hold it.
	PullMissing PullPolicy = "missing"

	// PullAlways pulls the image even if the host holds it, so that a tag
	// that has moved is followed. Nothing is downloaded if the host already
	// has what the tag points at.
	PullAlways PullPolicy = "always"

	// PullNever uses the image the host holds, and fails if it holds none.
	PullNever PullPolicy = "never"
)

// PullStage is the part of a pull that is currently working.
type PullStage string

const (
	// StageResolving is asking the registry what the reference points at.
	StageResolving PullStage = "resolving"

	// StageDownloading is fetching the layers, the only stage with a byte
	// count worth reporting.
	StageDownloading PullStage = "downloading"

	// StageUnpacking is writing those layers out as a root filesystem.
	StageUnpacking PullStage = "unpacking"

	// StageConverting is packing that filesystem into the disk a guest
	// boots from.
	StageConverting PullStage = "converting"
)

// PullProgress reports how far a pull has got. DownloadedBytes and
// TotalBytes are compressed layer bytes, and are zero outside
// StageDownloading.
type PullProgress struct {
	Stage           PullStage
	DownloadedBytes int64
	TotalBytes      int64

	// Image is the image that was pulled, set on the last message of a
	// pull and nil before it.
	Image *Image
}

// PruneResult is what a prune removed.
type PruneResult struct {
	// Images are the images that were removed.
	Images []Image

	// ReclaimedBytes is the disk their bootable disks occupied, plus what
	// the layer cache gave back.
	ReclaimedBytes int64
}
