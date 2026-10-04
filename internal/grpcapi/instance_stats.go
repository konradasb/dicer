// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// instanceStatsInterval is how often instance stats are read, and so the
// period CPU use is measured over.
const instanceStatsInterval = time.Second

// GetInstanceStats streams what instances use of the host. Stats are read
// once before the first batch, so that each batch can say how much CPU was
// used since the read before; an instance read for the first time, or
// started again since, is left out of the batch until it has been read
// twice.
func (h *instanceHandler) GetInstanceStats(
	req *dicerdv1.GetInstanceStatsRequest,
	stream grpc.ServerStreamingServer[dicerdv1.GetInstanceStatsResponse],
) error {
	// Names are resolved once, so a rename does not lose an instance.
	var wanted map[string]bool
	for _, name := range req.GetNames() {
		inst, err := h.definitions.GetInstance(name)
		if err != nil {
			return err
		}
		if wanted == nil {
			wanted = make(map[string]bool, len(req.GetNames()))
		}
		wanted[inst.ID] = true
	}

	ticker := time.NewTicker(h.statsInterval)
	defer ticker.Stop()

	previous := h.readStats(wanted)
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-ticker.C:
		}

		current := h.readStats(wanted)
		if err := stream.Send(instanceStatsBatch(previous, current)); err != nil {
			return err
		}
		if !req.GetFollow() {
			return nil
		}
		previous = current
	}
}

// readStats reads the stats of the instances in wanted, by instance ID, or
// of every instance if wanted is nil.
func (h *instanceHandler) readStats(wanted map[string]bool) []types.InstanceStats {
	stats := h.instances.Stats()
	if wanted == nil {
		return stats
	}

	kept := stats[:0]
	for _, s := range stats {
		if wanted[s.InstanceID] {
			kept = append(kept, s)
		}
	}
	return kept
}

// instanceStatsBatch converts current into a batch, each instance's CPU use
// measured since it was read in previous. An instance without a comparable
// read in previous is left out.
func instanceStatsBatch(previous, current []types.InstanceStats) *dicerdv1.GetInstanceStatsResponse {
	byID := make(map[string]types.InstanceStats, len(previous))
	for _, s := range previous {
		byID[s.InstanceID] = s
	}

	batch := &dicerdv1.GetInstanceStatsResponse{
		ReadTime:  timestamppb.Now(),
		Instances: make([]*dicerdv1.InstanceStats, 0, len(current)),
	}
	for _, s := range current {
		cpuPercent, ok := s.CPUPercent(byID[s.InstanceID])
		if !ok {
			continue
		}

		batch.Instances = append(batch.Instances, &dicerdv1.InstanceStats{
			Name:                   s.Name,
			Id:                     s.InstanceID,
			CpuPercent:             cpuPercent,
			CpuTime:                durationpb.New(s.CPUTime),
			Vcpus:                  int32(s.Committed.VCPUs),
			ResidentMemoryBytes:    s.ResidentMemoryBytes,
			MemoryBytes:            s.Committed.MemoryBytes,
			DiskReadBytes:          s.DiskReadBytes,
			DiskWrittenBytes:       s.DiskWrittenBytes,
			NetworkReceiveBytes:    s.NetworkReceiveBytes,
			NetworkTransmitBytes:   s.NetworkTransmitBytes,
			NetworkReceiveDrops:    s.NetworkReceiveDrops,
			NetworkTransmitDrops:   s.NetworkTransmitDrops,
			NetworkReceiveErrors:   s.NetworkReceiveErrors,
			NetworkTransmitErrors:  s.NetworkTransmitErrors,
			NetworkReceivePackets:  s.NetworkReceivePackets,
			NetworkTransmitPackets: s.NetworkTransmitPackets,
		})
	}

	return batch
}
