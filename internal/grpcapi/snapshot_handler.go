// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"cmp"
	"context"
	"time"

	"github.com/nrednav/cuid2"
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
	if req.GetInstance() == "" {
		return nil, errdefs.InvalidArgument("instance is required")
	}
	instance, err := h.definitions.Instance(req.GetInstance())
	if err != nil {
		return nil, err
	}

	snapshot, err := h.instances.CreateSnapshot(ctx, instance, req.GetName())
	if err != nil {
		return nil, err
	}

	return snapshotToProto(snapshot, instance.Name), nil
}

// ListSnapshots lists the snapshots, or those of one instance.
func (h *snapshotHandler) ListSnapshots(
	_ context.Context, req *dicerdv1.ListSnapshotsRequest,
) (*dicerdv1.ListSnapshotsResponse, error) {
	var instanceID string
	if req.GetInstance() != "" {
		instance, err := h.definitions.Instance(req.GetInstance())
		if err != nil {
			return nil, err
		}
		instanceID = instance.ID
	}

	resp := &dicerdv1.ListSnapshotsResponse{}
	for _, snapshot := range h.instances.Snapshots() {
		if instanceID == "" || snapshot.Instance.ID == instanceID {
			resp.Snapshots = append(resp.Snapshots, snapshotToProto(snapshot, h.instanceName(snapshot)))
		}
	}

	return resp, nil
}

// GetSnapshot returns a snapshot.
func (h *snapshotHandler) GetSnapshot(
	_ context.Context, req *dicerdv1.GetSnapshotRequest,
) (*dicerdv1.Snapshot, error) {
	snapshot, err := h.snapshot(req.GetName())
	if err != nil {
		return nil, err
	}

	return snapshotToProto(snapshot, h.instanceName(snapshot)), nil
}

// DeleteSnapshot removes a snapshot.
func (h *snapshotHandler) DeleteSnapshot(
	ctx context.Context, req *dicerdv1.DeleteSnapshotRequest,
) (*emptypb.Empty, error) {
	snapshot, err := h.snapshot(req.GetName())
	if err != nil {
		return nil, err
	}

	if err := h.instances.DeleteSnapshot(ctx, snapshot); err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

// RestoreSnapshot puts the instance a snapshot was taken of back as it was.
func (h *snapshotHandler) RestoreSnapshot(
	ctx context.Context, req *dicerdv1.RestoreSnapshotRequest,
) (*dicerdv1.Instance, error) {
	snapshot, err := h.snapshot(req.GetName())
	if err != nil {
		return nil, err
	}

	instance, err := h.instances.RestoreSnapshot(ctx, snapshot)
	if err != nil {
		return nil, err
	}

	return viewInstance(h.instances, instance)
}

// ForkSnapshot creates an instance as a copy of the one a snapshot was taken
// of.
func (h *snapshotHandler) ForkSnapshot(
	ctx context.Context, req *dicerdv1.ForkSnapshotRequest,
) (*dicerdv1.Instance, error) {
	snapshot, err := h.snapshot(req.GetName())
	if err != nil {
		return nil, err
	}
	fork, err := forkDefinition(snapshot, req)
	if err != nil {
		return nil, err
	}
	// What a created instance is checked for applies to a fork as much.
	creation := instanceHandler{definitions: h.definitions, instances: h.instances}
	if err := creation.checkCanStart(fork); err != nil {
		return nil, err
	}

	if err := h.instances.Fork(ctx, snapshot, fork); err != nil {
		return nil, err
	}

	return viewInstance(h.instances, fork)
}

// forkDefinition returns the definition of the instance a fork request
// makes of snapshot: its instance's, less what was that instance's alone,
// its ID and name, its address, and its host ports.
func forkDefinition(snapshot types.Snapshot, req *dicerdv1.ForkSnapshotRequest) (types.InstanceSpec, error) {
	ports, err := portMappingsFromProto(req.GetPorts())
	if err != nil {
		return types.InstanceSpec{}, err
	}

	now := time.Now()
	fork := snapshot.Instance
	fork.ID, fork.Name = cuid2.Generate(), req.GetInstance()
	fork.NetworkName = cmp.Or(req.GetNetworkName(), fork.NetworkName)
	fork.StaticIP = req.GetStaticIp()
	fork.Ports = ports
	fork.StoppedByUser = false
	fork.CreatedAt, fork.UpdatedAt = now, now

	if err := fork.Validate(); err != nil {
		return types.InstanceSpec{}, err
	}
	return fork, nil
}

// snapshot resolves the snapshot a request names.
func (h *snapshotHandler) snapshot(nameOrID string) (types.Snapshot, error) {
	if nameOrID == "" {
		return types.Snapshot{}, errdefs.InvalidArgument("snapshot name is required")
	}

	return h.instances.Snapshot(nameOrID)
}

// instanceName returns the name a snapshot's instance has now, or, if it
// has been deleted, the name it had.
func (h *snapshotHandler) instanceName(snapshot types.Snapshot) string {
	if instance, err := h.definitions.Instance(snapshot.Instance.ID); err == nil {
		return instance.Name
	}
	return snapshot.Instance.Name
}
