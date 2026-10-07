// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"os"

	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// hostHandler reports the daemon's version, hypervisors and addresses.
type hostHandler struct {
	version     string
	hypervisors map[types.HypervisorType][]hypervisor.Starter
	apiAddress  string
}

// GetHostInfo reports the daemon's version, hostname, hypervisors and API
// addresses.
func (h *hostHandler) GetHostInfo(
	_ context.Context, _ *dicerdv1.GetHostInfoRequest,
) (*dicerdv1.GetHostInfoResponse, error) {
	hostname, _ := os.Hostname()

	return &dicerdv1.GetHostInfoResponse{
		Version:      h.version,
		Hostname:     hostname,
		Hypervisors:  h.hypervisorInfos(),
		ApiAddresses: h.apiAddresses(),
	}, nil
}

// hypervisorInfos describes the available hypervisors, the default first.
func (h *hostHandler) hypervisorInfos() []*dicerdv1.HypervisorInfo {
	out := make([]*dicerdv1.HypervisorInfo, 0, len(h.hypervisors))

	for i, hypervisorType := range types.HypervisorTypes() {
		starters, ok := h.hypervisors[hypervisorType]
		if !ok {
			continue
		}

		versions := make([]string, 0, len(starters))
		for _, s := range starters {
			versions = append(versions, s.Version())
		}

		out = append(out, &dicerdv1.HypervisorInfo{
			Type:               hypervisorTypes.toProto(hypervisorType),
			Versions:           versions,
			IsDefault:          i == 0,
			DeprecatedVersions: hypervisor.DeprecatedVersions(starters),
		})
	}

	return out
}

// apiAddresses returns the daemon's TCP addresses.
func (h *hostHandler) apiAddresses() []string {
	if h.apiAddress == "" {
		return nil
	}

	return []string{h.apiAddress}
}
