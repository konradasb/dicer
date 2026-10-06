// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"cmp"
	"context"
	"time"

	"github.com/nrednav/cuid2"

	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// ForkInstance creates an instance as a copy of another. See
// vm.Manager.ForkInstance.
func (h *instanceHandler) ForkInstance(
	ctx context.Context, req *dicerdv1.ForkInstanceRequest,
) (*dicerdv1.Instance, error) {
	source, err := h.definitions.Instance(req.GetName())
	if err != nil {
		return nil, err
	}
	fork, err := forkDefinition(source, req)
	if err != nil {
		return nil, err
	}
	if err := h.checkCanStart(fork); err != nil {
		return nil, err
	}

	if err := h.instances.ForkInstance(ctx, source, fork); err != nil {
		return nil, err
	}

	return h.view(fork)
}

// forkRequest is what a request to fork an instance or a snapshot says of
// the new instance.
type forkRequest interface {
	GetForkName() string
	GetNetworkName() string
	GetStaticIp() string
	GetPorts() []*dicerdv1.PortMapping
}

// forkDefinition returns the definition of the instance a fork request makes
// of source: source's, less what was source's alone, its ID and name, its
// address, and its host ports.
func forkDefinition(source types.InstanceSpec, req forkRequest) (types.InstanceSpec, error) {
	ports, err := portMappingsFromProto(req.GetPorts())
	if err != nil {
		return types.InstanceSpec{}, err
	}

	now := time.Now()
	fork := source
	fork.ID, fork.Name = cuid2.Generate(), req.GetForkName()
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
