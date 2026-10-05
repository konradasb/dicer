// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// snapshotHandler handles snapshot-related RPCs.
type snapshotHandler struct {
	definitions *filestore.Manager
	instances   *vm.Manager
}

// CreateSnapshot snapshots an instance.
func (h *snapshotHandler) CreateSnapshot(
	ctx context.Context, req *dicerdv1.CreateSnapshotRequest,
) (*dicerdv1.Snapshot, error) {
	instance, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	snapshot, err := h.instances.CreateSnapshot(ctx, instance, req.GetName())
	if err != nil {
		return nil, err
	}

	return snapshotToProto(snapshot, instance.Name), nil
}

// ListSnapshots lists an instance's snapshots.
func (h *snapshotHandler) ListSnapshots(
	_ context.Context, req *dicerdv1.ListSnapshotsRequest,
) (*dicerdv1.ListSnapshotsResponse, error) {
	instance, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	snapshots, err := h.instances.Snapshots(instance)
	if err != nil {
		return nil, err
	}

	resp := &dicerdv1.ListSnapshotsResponse{
		Snapshots: make([]*dicerdv1.Snapshot, 0, len(snapshots)),
	}
	for _, snapshot := range snapshots {
		resp.Snapshots = append(resp.Snapshots, snapshotToProto(snapshot, instance.Name))
	}

	return resp, nil
}

// GetSnapshot returns one of an instance's snapshots.
func (h *snapshotHandler) GetSnapshot(
	_ context.Context, req *dicerdv1.GetSnapshotRequest,
) (*dicerdv1.Snapshot, error) {
	instance, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	snapshot, err := h.instances.Snapshot(instance, req.GetName())
	if err != nil {
		return nil, err
	}

	return snapshotToProto(snapshot, instance.Name), nil
}

// DeleteSnapshot removes one of an instance's snapshots.
func (h *snapshotHandler) DeleteSnapshot(
	ctx context.Context, req *dicerdv1.DeleteSnapshotRequest,
) (*emptypb.Empty, error) {
	instance, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	if err := h.instances.DeleteSnapshot(ctx, instance, req.GetName()); err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

// RestoreSnapshot rolls an instance back to one of its snapshots.
func (h *snapshotHandler) RestoreSnapshot(
	ctx context.Context, req *dicerdv1.RestoreSnapshotRequest,
) (*dicerdv1.Instance, error) {
	instance, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	if err := h.instances.RestoreSnapshot(ctx, instance, req.GetName()); err != nil {
		return nil, err
	}

	return viewInstance(h.instances, instance)
}

// instance resolves the instance a snapshot request names.
func (h *snapshotHandler) instance(nameOrID string) (types.InstanceSpec, error) {
	if nameOrID == "" {
		return types.InstanceSpec{}, errdefs.InvalidArgument("instance is required")
	}

	return h.definitions.Instance(nameOrID)
}
