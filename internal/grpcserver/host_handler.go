// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcserver

import (
	"cmp"
	"context"
	"iter"
	"net/netip"
	"os"
	"slices"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/doctor"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/token"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// hostHandler reports the daemon's version, hypervisors and addresses.
type hostHandler struct {
	version       string
	starters      map[hypervisor.Type][]hypervisor.Starter
	listenAddress string
	hostAddresses func() ([]netip.Addr, error)
	fingerprint   string
	checkHost     func(ctx context.Context, opts doctor.Options) iter.Seq[doctor.Result]
}

// CheckHost checks that the host can run instances and reach them, and
// boots a test instance if the request asks for one, sending each result as
// it is found. Booting a test instance needs instances:write.
func (h *hostHandler) CheckHost(
	req *dicerdv1.CheckHostRequest, stream grpc.ServerStreamingServer[dicerdv1.HostCheckResult],
) error {
	hypervisorType, err := hypervisorTypes.fromProto(req.GetTestInstanceHypervisor())
	if err != nil {
		return err
	}
	opts := doctor.Options{
		TestInstance:                  req.GetTestInstance(),
		TestInstanceImage:             req.GetTestInstanceImage(),
		TestInstanceHypervisor:        hypervisorType,
		TestInstanceHypervisorVersion: req.GetTestInstanceHypervisorVersion(),
		TestInstanceTimeout:           req.GetTestInstanceTimeout().AsDuration(),
		KeepFailedTestInstance:        req.GetKeepFailedTestInstance(),
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	// A call over the socket has no token, and may.
	if t, ok := tokenFrom(stream.Context()); ok && opts.TestInstance && !t.Allows(token.ScopeInstancesWrite) {
		return errdefs.PermissionDenied("token %q lacks scope %s, which booting a test instance needs",
			t.Name, token.ScopeInstancesWrite)
	}
	if opts.TestInstance {
		if err := h.checkCarries(cmp.Or(hypervisorType, hypervisor.DefaultType), opts.TestInstanceHypervisorVersion); err != nil {
			return err
		}
	}
	if h.checkHost == nil {
		return nil
	}

	for r := range h.checkHost(stream.Context(), opts) {
		if err := stream.Send(hostCheckResultToProto(r)); err != nil {
			return err
		}
	}
	return nil
}

// checkCarries returns an errdefs.ErrInvalidArgument error unless the
// daemon carries a hypervisor's version. An empty version is the
// hypervisor's default.
func (h *hostHandler) checkCarries(hypervisorType hypervisor.Type, version string) error {
	starters := h.starters[hypervisorType]
	if len(starters) == 0 {
		return errdefs.InvalidArgument("hypervisor %s is not on this host: name one 'dicer info' lists", hypervisorType)
	}
	if version != "" && !slices.ContainsFunc(starters, func(s hypervisor.Starter) bool { return s.Version() == version }) {
		return errdefs.InvalidArgument("hypervisor %s %s is not on this host: name a version 'dicer info' lists",
			hypervisorType, version)
	}
	return nil
}

// hostCheckResultToProto converts what one check of the host found.
func hostCheckResultToProto(r doctor.Result) *dicerdv1.HostCheckResult {
	return &dicerdv1.HostCheckResult{
		Group:   hostCheckGroups.toProto(r.Group),
		Name:    r.Name,
		Status:  hostCheckStatuses.toProto(r.Status),
		Detail:  r.Detail,
		Hint:    r.Hint,
		Console: r.Console,
	}
}

// GetHostInfo reports the daemon's version, hostname, hypervisors and API
// addresses, and the token the call was made with.
func (h *hostHandler) GetHostInfo(
	ctx context.Context, _ *dicerdv1.GetHostInfoRequest,
) (*dicerdv1.GetHostInfoResponse, error) {
	hostname, _ := os.Hostname()
	// A call over the socket has no token, and so no token's name.
	t, _ := tokenFrom(ctx)

	return &dicerdv1.GetHostInfoResponse{
		Version:           h.version,
		Hostname:          hostname,
		Hypervisors:       h.hypervisorInfos(),
		ListenerAddresses: h.listenerAddresses(),
		Fingerprint:       h.fingerprint,
		Token:             t.Name,
	}, nil
}

// hypervisorInfos describes the available hypervisors, the default first.
func (h *hostHandler) hypervisorInfos() []*dicerdv1.HypervisorInfo {
	out := make([]*dicerdv1.HypervisorInfo, 0, len(h.starters))

	for i, hypervisorType := range hypervisor.Types() {
		starters, ok := h.starters[hypervisorType]
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

// listenerAddresses returns the addresses of the TCP listener: the one it
// is bound to, or, for a listener on all of the host's addresses, those
// addresses with its port. It returns none if the API is not served over
// TCP.
func (h *hostHandler) listenerAddresses() []string {
	if h.listenAddress == "" {
		return nil
	}

	listener, err := netip.ParseAddrPort(h.listenAddress)
	if err != nil || !listener.Addr().IsUnspecified() || h.hostAddresses == nil {
		return []string{h.listenAddress}
	}
	addrs, err := h.hostAddresses()
	if err != nil || len(addrs) == 0 {
		return []string{h.listenAddress}
	}

	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, netip.AddrPortFrom(a, listener.Port()).String())
	}
	return out
}
