// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/types"
	volumepkg "github.com/konradasb/dicer/internal/volume"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// volumeHandler handles volume-related RPCs.
type volumeHandler struct {
	definitions *filestore.Manager
	volumes     *volumepkg.Manager
	events      recorder
}

// CreateVolume creates and formats a volume.
func (h *volumeHandler) CreateVolume(
	ctx context.Context, req *dicerdv1.CreateVolumeRequest,
) (*dicerdv1.Volume, error) {
	if err := naming.Validate(req.GetName()); err != nil {
		return nil, err
	}
	if req.GetSizeBytes() <= 0 {
		return nil, errdefs.InvalidArgument("size_bytes must be greater than 0")
	}

	if _, err := h.definitions.Volume(req.GetName()); err == nil {
		return nil, errdefs.Exists("volume %q already exists", req.GetName())
	}

	volume, err := h.volumes.Create(ctx, req.GetName(), req.GetSizeBytes())
	if err != nil {
		return nil, fmt.Errorf("create volume: %w", err)
	}

	if err := h.definitions.CreateVolume(*volume); err != nil {
		// Roll back the backing disk so a failed create leaves nothing behind.
		_ = h.volumes.Delete(volume.ID)
		return nil, err
	}
	h.record(*volume, events.ActionCreated, "Created volume of "+humanize.Bytes(volume.SizeBytes)+", formatted ext4")

	return volumeToProto(*volume), nil
}

// ListVolumes lists the volumes, sorted by name.
func (h *volumeHandler) ListVolumes(
	_ context.Context, _ *dicerdv1.ListVolumesRequest,
) (*dicerdv1.ListVolumesResponse, error) {
	volumes := h.definitions.Volumes()

	resp := &dicerdv1.ListVolumesResponse{
		Volumes: make([]*dicerdv1.Volume, 0, len(volumes)),
	}
	for _, v := range volumes {
		resp.Volumes = append(resp.Volumes, volumeToProto(v))
	}

	return resp, nil
}

// GetVolume returns a volume.
func (h *volumeHandler) GetVolume(
	_ context.Context, req *dicerdv1.GetVolumeRequest,
) (*dicerdv1.Volume, error) {
	volume, err := h.definitions.Volume(req.GetName())
	if err != nil {
		return nil, err
	}

	return volumeToProto(volume), nil
}

// DeleteVolume removes a volume and its data, refusing one an instance
// mounts.
func (h *volumeHandler) DeleteVolume(
	_ context.Context, req *dicerdv1.DeleteVolumeRequest,
) (*emptypb.Empty, error) {
	volume, err := h.definitions.Volume(req.GetName())
	if err != nil {
		return nil, err
	}

	mounted := func(instance types.InstanceSpec) bool {
		_, ok := instance.VolumeMount(volume.Name)
		return ok
	}
	if err := refuseInUse(h.definitions, fmt.Sprintf("volume %q is mounted", volume.Name), mounted); err != nil {
		return nil, err
	}

	if err := h.definitions.DeleteVolume(volume.Name); err != nil {
		return nil, err
	}
	h.record(volume, events.ActionDeleted, "Deleted volume of "+humanize.Bytes(volume.SizeBytes)+" and its data")

	if err := h.volumes.Delete(volume.ID); err != nil {
		return nil, fmt.Errorf("remove volume disk: %w", err)
	}

	return &emptypb.Empty{}, nil
}

// record records that action happened to volume, with its size among the
// attributes.
func (h *volumeHandler) record(volume types.Volume, action events.Action, message string) {
	h.events.Record(events.Event{
		Kind:       events.KindVolume,
		ID:         volume.ID,
		Name:       volume.Name,
		Action:     action,
		Message:    message,
		Attributes: map[string]string{"size_bytes": strconv.FormatInt(volume.SizeBytes, 10)},
	})
}
