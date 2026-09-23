// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/volume"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// volumeHandler handles volume-related RPCs.
type volumeHandler struct {
	definitions *filestore.Manager
	volumes     *volume.Manager
}

func (h *volumeHandler) CreateVolume(
	ctx context.Context, req *dicerdv1.CreateVolumeRequest,
) (*dicerdv1.Volume, error) {
	if err := dicer.ValidateName(req.GetName()); err != nil {
		return nil, toStatus(err)
	}
	if req.GetSizeBytes() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "size_bytes must be greater than 0")
	}

	if _, err := h.definitions.GetVolume(req.GetName()); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "volume %q already exists", req.GetName())
	}

	vol, err := h.volumes.Create(ctx, req.GetName(), req.GetSizeBytes())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create volume: %v", err)
	}

	if err := h.definitions.CreateVolume(*vol); err != nil {
		// Roll back the backing disk so a failed create leaves nothing behind.
		_ = h.volumes.Delete(vol.ID)
		return nil, toStatus(err)
	}

	return volumeToProto(*vol), nil
}

func (h *volumeHandler) ListVolumes(
	_ context.Context, _ *dicerdv1.ListVolumesRequest,
) (*dicerdv1.ListVolumesResponse, error) {
	volumes, err := h.definitions.ListVolumes()
	if err != nil {
		return nil, toStatus(err)
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
		return nil, toStatus(err)
	}

	return volumeToProto(vol), nil
}

func (h *volumeHandler) DeleteVolume(
	_ context.Context, req *dicerdv1.DeleteVolumeRequest,
) (*emptypb.Empty, error) {
	vol, err := h.definitions.GetVolume(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	mounted := func(inst dicer.InstanceSpec) bool {
		return slices.ContainsFunc(inst.VolumeMounts, func(m dicer.VolumeMount) bool { return m.VolumeName == vol.Name })
	}
	if err := refuseInUse(h.definitions, fmt.Sprintf("volume %q is mounted", vol.Name), mounted); err != nil {
		return nil, err
	}

	if err := h.definitions.DeleteVolume(vol.Name); err != nil {
		return nil, toStatus(err)
	}

	if err := h.volumes.Delete(vol.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "remove volume disk: %v", err)
	}

	return &emptypb.Empty{}, nil
}
