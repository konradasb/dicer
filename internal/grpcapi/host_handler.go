// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"os"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/access"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// hostHandler reports what the daemon is: its version, the hypervisors it
// carries, and how it is reached. How much of the host is in use is
// resourceHandler's.
type hostHandler struct {
	version     string
	hypervisors map[dicer.HypervisorType][]hypervisor.Starter
	access      *access.Manager
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
		ApiFingerprint: h.access.Fingerprint(),
		ApiAddresses:   h.access.Addresses(),
		DefaultKernel:  h.defaults.kernel(),
		DefaultNetwork: h.defaults.network(),
	}, nil
}

// hypervisorInfo reports the hypervisors the daemon can start instances
// with, in the order dicer.HypervisorTypes lists them, so the default comes
// first and the listing is stable.
func (h *hostHandler) hypervisorInfo() []*dicerdv1.HypervisorInfo {
	out := make([]*dicerdv1.HypervisorInfo, 0, len(h.hypervisors))

	for i, hvType := range dicer.HypervisorTypes() {
		starters, ok := h.hypervisors[hvType]
		if !ok {
			continue
		}

		versions := make([]string, 0, len(starters))
		for _, s := range starters {
			versions = append(versions, s.Version())
		}

		out = append(out, &dicerdv1.HypervisorInfo{
			Type:      string(hvType),
			Versions:  versions,
			IsDefault: i == 0,
		})
	}

	return out
}
