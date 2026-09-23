// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// hostInfoFromProto is HostInfoToProto backwards.
func hostInfoFromProto(p *dicerdv1.GetHostInfoResponse) HostInfo {
	if p == nil {
		return HostInfo{}
	}

	out := HostInfo{
		Version:        p.GetVersion(),
		Hostname:       p.GetHostname(),
		APIFingerprint: p.GetApiFingerprint(),
		APIAddresses:   p.GetApiAddresses(),
		DefaultKernel:  p.GetDefaultKernel(),
		DefaultNetwork: p.GetDefaultNetwork(),
	}

	for _, hv := range p.GetHypervisors() {
		out.Hypervisors = append(out.Hypervisors, HypervisorInfo{
			Type:      HypervisorType(hv.GetType()),
			Versions:  hv.GetVersions(),
			IsDefault: hv.GetIsDefault(),
		})
	}

	return out
}

// HostResourcesToProto converts what the host has and what its instances
// hold of it.

// hostResourcesFromProto is HostResourcesToProto backwards.
func hostResourcesFromProto(p *dicerdv1.GetResourcesResponse) HostResources {
	if p == nil {
		return HostResources{}
	}

	out := HostResources{
		CPU:    capacityFromProto(p.GetCpu()),
		Memory: capacityFromProto(p.GetMemory()),
		Disk: HostDisk{
			Path:             p.GetDisk().GetPath(),
			TotalBytes:       p.GetDisk().GetTotalBytes(),
			FreeBytes:        p.GetDisk().GetFreeBytes(),
			ProvisionedBytes: p.GetDisk().GetProvisionedBytes(),
		},
	}

	for _, h := range p.GetInstances() {
		out.Instances = append(out.Instances, Holder{
			Name:  h.GetName(),
			State: InstanceState(h.GetState()),
			Resources: Resources{
				VCPUs:       int(h.GetVcpus()),
				MemoryBytes: h.GetMemoryBytes(),
			},
		})
	}

	return out
}

func capacityFromProto(p *dicerdv1.ResourceCapacity) ResourceCapacity {
	return ResourceCapacity{
		Host:        p.GetHost(),
		Reserved:    p.GetReserved(),
		Overcommit:  p.GetOvercommit(),
		Allocatable: p.GetAllocatable(),
		Allocated:   p.GetAllocated(),
		Available:   p.GetAvailable(),
	}
}

// EventToProto converts one thing that happened to one resource.

// eventFromProto is EventToProto backwards.
func eventFromProto(p *dicerdv1.Event) Event {
	if p == nil {
		return Event{}
	}

	return Event{
		Time:       goTime(p.GetTime()),
		Kind:       EventKind(p.GetKind()),
		ID:         p.GetId(),
		Name:       p.GetName(),
		Action:     EventAction(p.GetAction()),
		Message:    p.GetMessage(),
		Attributes: p.GetAttributes(),
	}
}
