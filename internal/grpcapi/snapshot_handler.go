// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/vm"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// snapshotHandler handles snapshot-related RPCs. Snapshots belong to an
// instance, so every request names one.
type snapshotHandler struct {
	definitions *filestore.Manager
	instances   *vm.Manager
	logger      *slog.Logger
}

func (h *snapshotHandler) CreateSnapshot(
	ctx context.Context, req *dicerdv1.CreateSnapshotRequest,
) (*dicerdv1.Snapshot, error) {
	inst, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	snap, err := h.instances.CreateSnapshot(ctx, inst, req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	return snapshotToProto(snap, inst.Name), nil
}

func (h *snapshotHandler) ListSnapshots(
	_ context.Context, req *dicerdv1.ListSnapshotsRequest,
) (*dicerdv1.ListSnapshotsResponse, error) {
	inst, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	snapshots, err := h.instances.ListSnapshots(inst)
	if err != nil {
		return nil, toStatus(err)
	}

	resp := &dicerdv1.ListSnapshotsResponse{
		Snapshots: make([]*dicerdv1.Snapshot, 0, len(snapshots)),
	}
	for _, snap := range snapshots {
		resp.Snapshots = append(resp.Snapshots, snapshotToProto(snap, inst.Name))
	}

	return resp, nil
}

func (h *snapshotHandler) GetSnapshot(
	_ context.Context, req *dicerdv1.GetSnapshotRequest,
) (*dicerdv1.Snapshot, error) {
	inst, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	snap, err := h.instances.GetSnapshot(inst, req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	return snapshotToProto(snap, inst.Name), nil
}

func (h *snapshotHandler) DeleteSnapshot(
	ctx context.Context, req *dicerdv1.DeleteSnapshotRequest,
) (*emptypb.Empty, error) {
	inst, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	if err := h.instances.DeleteSnapshot(ctx, inst, req.GetName()); err != nil {
		return nil, toStatus(err)
	}

	return &emptypb.Empty{}, nil
}

func (h *snapshotHandler) RestoreSnapshot(
	ctx context.Context, req *dicerdv1.RestoreSnapshotRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	if err := h.instances.RestoreSnapshot(ctx, inst, req.GetName()); err != nil {
		h.logger.ErrorContext(ctx, "restore failed",
			"instance", inst.Name, "snapshot", req.GetName(), "error", err)
		return nil, toStatus(err)
	}

	return viewInstance(h.instances, inst)
}

// instance resolves the instance a snapshot request names.
func (h *snapshotHandler) instance(nameOrID string) (dicer.InstanceSpec, error) {
	if nameOrID == "" {
		return dicer.InstanceSpec{}, status.Error(codes.InvalidArgument, "instance is required")
	}

	inst, err := h.definitions.GetInstance(nameOrID)
	if err != nil {
		return dicer.InstanceSpec{}, toStatus(err)
	}

	return inst, nil
}
