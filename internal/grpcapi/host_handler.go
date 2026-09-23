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
	defaults    defaultResolver
}

func (h *hostHandler) GetHostInfo(
	_ context.Context, _ *dicerdv1.GetHostInfoRequest,
) (*dicerdv1.GetHostInfoResponse, error) {
	hostname, _ := os.Hostname()

	return &dicerdv1.GetHostInfoResponse{
		Version:        h.version,
		Hostname:       hostname,
		Hypervisors:    h.hypervisorInfo(),
		ApiAddresses:   h.apiAddresses(),
		DefaultKernel:  h.defaults.kernel(),
		DefaultNetwork: h.defaults.network(),
	}, nil
}

// hypervisorInfo lists the available hypervisors, the default first.
func (h *hostHandler) hypervisorInfo() []*dicerdv1.HypervisorInfo {
	out := make([]*dicerdv1.HypervisorInfo, 0, len(h.hypervisors))

	for i, hvType := range types.HypervisorTypes() {
		starters, ok := h.hypervisors[hvType]
		if !ok {
			continue
		}

		versions := make([]string, 0, len(starters))
		for _, s := range starters {
			versions = append(versions, s.Version())
		}

		out = append(out, &dicerdv1.HypervisorInfo{
			Type:      hypervisorTypes.toProto(hvType),
			Versions:  versions,
			IsDefault: i == 0,
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
