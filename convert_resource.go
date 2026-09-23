// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// imageFromProto is ImageToProto backwards.
func imageFromProto(p *dicerdv1.Image) Image {
	if p == nil {
		return Image{}
	}

	return Image{
		Name:        p.GetName(),
		Digest:      p.GetDigest(),
		SizeBytes:   p.GetSizeBytes(),
		HealthCheck: healthCheckFromProto(p.GetHealthCheck()),
		CreatedAt:   goTime(p.GetCreateTime()),
		UpdatedAt:   goTime(p.GetUpdateTime()),
		LastUsedAt:  goTime(p.GetLastUsedTime()),
	}
}

// PullProgressToProto converts how far a pull has got.

// pullProgressFromProto is PullProgressToProto backwards.
func pullProgressFromProto(p *dicerdv1.PullImageProgress) PullProgress {
	if p == nil {
		return PullProgress{}
	}

	out := PullProgress{
		Stage:           pullStageFromProto(p.GetStage()),
		DownloadedBytes: p.GetDownloadedBytes(),
		TotalBytes:      p.GetTotalBytes(),
	}
	if img := p.GetImage(); img != nil {
		converted := imageFromProto(img)
		out.Image = &converted
	}

	return out
}

// pullStages pairs each stage of a pull with its enum value. An unknown
// stage on either side is the unspecified one, which is what a client too
// old to know a stage should see.

// pullStages pairs each stage of a pull with its enum value. An unknown
// stage on either side is the unspecified one, which is what a client too
// old to know a stage should see.
var pullStages = []struct {
	stage PullStage
	enum  dicerdv1.PullStage
}{
	{StageResolving, dicerdv1.PullStage_PULL_STAGE_RESOLVING},
	{StageDownloading, dicerdv1.PullStage_PULL_STAGE_DOWNLOADING},
	{StageUnpacking, dicerdv1.PullStage_PULL_STAGE_UNPACKING},
	{StageConverting, dicerdv1.PullStage_PULL_STAGE_CONVERTING},
}

func pullStageFromProto(e dicerdv1.PullStage) PullStage {
	for _, p := range pullStages {
		if p.enum == e {
			return p.stage
		}
	}

	return ""
}

// VolumeToProto converts a volume.

// volumeFromProto is VolumeToProto backwards. The volume's path on the host
// is the daemon's business and is not on the wire.
func volumeFromProto(p *dicerdv1.Volume) Volume {
	if p == nil {
		return Volume{}
	}

	return Volume{
		ID:        p.GetId(),
		Name:      p.GetName(),
		SizeBytes: p.GetSizeBytes(),
		CreatedAt: goTime(p.GetCreateTime()),
		UpdatedAt: goTime(p.GetUpdateTime()),
	}
}

// KernelToProto converts a kernel.

// kernelFromProto is KernelToProto backwards.
func kernelFromProto(p *dicerdv1.Kernel) Kernel {
	if p == nil {
		return Kernel{}
	}

	return Kernel{
		ID:        p.GetId(),
		Name:      p.GetName(),
		Arch:      p.GetArch(),
		URL:       p.GetUrl(),
		SHA256:    p.GetSha256(),
		CreatedAt: goTime(p.GetCreateTime()),
		UpdatedAt: goTime(p.GetUpdateTime()),
	}
}

// NetworkToProto converts a network. TotalIPs and FreeIPs are the caller's to
// fill in: they are counted from the allocations, not stored.

// networkFromProto is NetworkToProto backwards.
func networkFromProto(p *dicerdv1.Network) Network {
	if p == nil {
		return Network{}
	}

	return Network{
		ID:          p.GetId(),
		Name:        p.GetName(),
		Subnet:      p.GetSubnet(),
		Gateway:     p.GetGateway(),
		Bridge:      p.GetBridge(),
		MTU:         int(p.GetMtu()),
		Nameservers: p.GetNameservers(),
		Isolated:    p.GetIsolated(),
		TotalIPs:    p.GetTotalIps(),
		FreeIPs:     p.GetFreeIps(),
		CreatedAt:   goTime(p.GetCreateTime()),
		UpdatedAt:   goTime(p.GetUpdateTime()),
	}
}

// AllocationToProto converts an address allocation. The network it is on is
// not on the wire: allocations are only ever listed for one network.

// allocationFromProto is AllocationToProto backwards.
func allocationFromProto(p *dicerdv1.NetworkAllocation) NetworkAllocation {
	if p == nil {
		return NetworkAllocation{}
	}

	return NetworkAllocation{
		InstanceID:   p.GetInstanceId(),
		InstanceName: p.GetInstanceName(),
		IP:           p.GetIp(),
		MAC:          p.GetMac(),
		TAPDevice:    p.GetTapDevice(),
	}
}

// SnapshotToProto converts a snapshot. What the guest ran with beyond its
// memory -- its vCPUs, the image digest -- is what a restore needs and not
// what a caller reads, so it is not on the wire.

// snapshotFromProto is SnapshotToProto backwards.
func snapshotFromProto(p *dicerdv1.Snapshot) Snapshot {
	if p == nil {
		return Snapshot{}
	}

	return Snapshot{
		Name:              p.GetName(),
		InstanceName:      p.GetInstanceName(),
		HypervisorType:    HypervisorType(p.GetHypervisorType()),
		HypervisorVersion: p.GetHypervisorVersion(),
		MemoryBytes:       p.GetMemoryBytes(),
		SizeBytes:         p.GetSizeBytes(),
		CreatedAt:         goTime(p.GetCreateTime()),
	}
}
