// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"

	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/hostinfo"
	"github.com/dicer-sh/dicer/internal/vm"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// resourceHandler reports how much of the host is in use.
//
// CPU and memory come from the lifecycle manager, which is what admits
// instances against them, so what is reported is exactly what admission
// sees. Disk is reported only: disks are sparse, and what an instance has
// been promised says little about what it will write.
type resourceHandler struct {
	definitions *filestore.Manager
	instances   *vm.Manager
	dataDir     string
}

func (h *resourceHandler) GetResources(
	_ context.Context, _ *dicerdv1.GetResourcesRequest,
) (*dicerdv1.GetResourcesResponse, error) {
	usage := h.instances.Usage()
	allocatable := usage.Capacity.Allocatable()
	available := usage.Available()

	resp := &dicerdv1.GetResourcesResponse{
		Cpu: &dicerdv1.ResourceCapacity{
			Host:        int64(usage.Capacity.Host.VCPUs),
			Overcommit:  usage.Capacity.CPUOvercommit,
			Allocatable: int64(allocatable.VCPUs),
			Allocated:   int64(usage.Allocated.VCPUs),
			Available:   int64(available.VCPUs),
		},
		Memory: &dicerdv1.ResourceCapacity{
			Host:        usage.Capacity.Host.MemoryBytes,
			Reserved:    usage.Capacity.ReservedMemoryBytes,
			Overcommit:  usage.Capacity.MemoryOvercommit,
			Allocatable: allocatable.MemoryBytes,
			Allocated:   usage.Allocated.MemoryBytes,
			Available:   available.MemoryBytes,
		},
		Instances: make([]*dicerdv1.InstanceResources, 0, len(usage.Holders)),
	}

	for _, holder := range usage.Holders {
		resp.Instances = append(resp.Instances, &dicerdv1.InstanceResources{
			Name:        holder.Name,
			State:       holder.State.String(),
			Vcpus:       int32(holder.Resources.VCPUs),
			MemoryBytes: holder.Resources.MemoryBytes,
		})
	}

	disk, err := h.diskUsage()
	if err != nil {
		return nil, toStatus(err)
	}
	resp.Disk = disk

	return resp, nil
}

// diskUsage reports on the data directory's filesystem, and what has been
// promised on it.
func (h *resourceHandler) diskUsage() (*dicerdv1.DiskUsage, error) {
	fs, err := hostinfo.ReadDiskUsage(h.dataDir)
	if err != nil {
		return nil, err
	}

	instances, err := h.definitions.ListInstances()
	if err != nil {
		return nil, err
	}
	volumes, err := h.definitions.ListVolumes()
	if err != nil {
		return nil, err
	}

	var provisioned int64
	for _, inst := range instances {
		provisioned += inst.DiskBytes
	}
	for _, v := range volumes {
		provisioned += v.SizeBytes
	}

	return &dicerdv1.DiskUsage{
		Path:             h.dataDir,
		TotalBytes:       fs.TotalBytes,
		FreeBytes:        fs.FreeBytes,
		ProvisionedBytes: provisioned,
	}, nil
}
