// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/volume"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// volumeHandler handles volume-related RPCs.
type volumeHandler struct {
	definitions *filestore.Manager
	volumes     *volume.Manager
}

func (h *volumeHandler) CreateVolume(
	ctx context.Context, req *dicerdv1.CreateVolumeRequest,
) (*dicerdv1.Volume, error) {
	if err := naming.Validate(req.GetName()); err != nil {
		return nil, err
	}
	if req.GetSizeBytes() <= 0 {
		return nil, errdefs.InvalidArgument("size_bytes must be greater than 0")
	}

	if _, err := h.definitions.GetVolume(req.GetName()); err == nil {
		return nil, errdefs.Exists("volume %q already exists", req.GetName())
	}

	vol, err := h.volumes.Create(ctx, req.GetName(), req.GetSizeBytes())
	if err != nil {
		return nil, fmt.Errorf("create volume: %w", err)
	}

	if err := h.definitions.CreateVolume(*vol); err != nil {
		// Roll back the backing disk so a failed create leaves nothing behind.
		_ = h.volumes.Delete(vol.ID)
		return nil, err
	}

	return volumeToProto(*vol), nil
}

func (h *volumeHandler) ListVolumes(
	_ context.Context, _ *dicerdv1.ListVolumesRequest,
) (*dicerdv1.ListVolumesResponse, error) {
	volumes, err := h.definitions.ListVolumes()
	if err != nil {
		return nil, err
	}

	resp := &dicerdv1.ListVolumesResponse{
		Volumes: make([]*dicerdv1.Volume, 0, len(volumes)),
	}
	for _, v := range volumes {
		resp.Volumes = append(resp.Volumes, volumeToProto(v))
	}

	return resp, nil
}

func (h *volumeHandler) GetVolume(
	_ context.Context, req *dicerdv1.GetVolumeRequest,
) (*dicerdv1.Volume, error) {
	vol, err := h.definitions.GetVolume(req.GetName())
	if err != nil {
		return nil, err
	}

	return volumeToProto(vol), nil
}

func (h *volumeHandler) DeleteVolume(
	_ context.Context, req *dicerdv1.DeleteVolumeRequest,
) (*emptypb.Empty, error) {
	vol, err := h.definitions.GetVolume(req.GetName())
	if err != nil {
		return nil, err
	}

	mounted := func(inst types.InstanceSpec) bool {
		_, ok := inst.MountsVolume(vol.Name)
		return ok
	}
	if err := refuseInUse(h.definitions, fmt.Sprintf("volume %q is mounted", vol.Name), mounted); err != nil {
		return nil, err
	}

	if err := h.definitions.DeleteVolume(vol.Name); err != nil {
		return nil, err
	}

	if err := h.volumes.Delete(vol.ID); err != nil {
		return nil, fmt.Errorf("remove volume disk: %w", err)
	}

	return &emptypb.Empty{}, nil
}
