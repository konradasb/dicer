// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/nrednav/cuid2"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// networkHandler handles network-related RPCs.
type networkHandler struct {
	definitions *filestore.Manager
	networks    *network.Manager
	hostSubnets func() ([]netip.Prefix, error)
	events      recorder
}

// CreateNetwork records a network, refusing a subnet another network or
// the host is on. Unset gateway, MTU and nameservers take their defaults.
func (h *networkHandler) CreateNetwork(
	_ context.Context, req *dicerdv1.CreateNetworkRequest,
) (*dicerdv1.Network, error) {
	if err := naming.Validate(req.GetName()); err != nil {
		return nil, err
	}
	if req.GetSubnet() == "" {
		return nil, errdefs.InvalidArgument("subnet is required")
	}

	if _, err := h.definitions.Network(req.GetName()); err == nil {
		return nil, errdefs.Exists("network %q already exists", req.GetName())
	}

	subnet, err := network.ParseSubnet(req.GetSubnet())
	if err != nil {
		return nil, err
	}
	gateway, err := networkGateway(subnet, req.GetGateway())
	if err != nil {
		return nil, err
	}
	if err := h.checkSubnetOverlap(subnet); err != nil {
		return nil, err
	}
	mtu, err := networkMTU(req.GetMtu())
	if err != nil {
		return nil, err
	}
	nameservers := req.GetNameservers()
	if !req.GetInternal() {
		if nameservers, err = networkNameservers(nameservers); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	n := types.Network{
		ID:          cuid2.Generate(),
		Name:        req.GetName(),
		Subnet:      subnet.String(),
		Gateway:     gateway,
		Bridge:      network.BridgeName(req.GetName()),
		MTU:         mtu,
		Nameservers: nameservers,
		Isolated:    req.GetIsolated(),
		Internal:    req.GetInternal(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := n.Validate(); err != nil {
		return nil, err
	}

	if err := h.definitions.CreateNetwork(n); err != nil {
		return nil, err
	}

	message := fmt.Sprintf("Created network with subnet %s, gateway %s", n.Subnet, n.Gateway)
	if n.Isolated {
		message += "; isolated: its instances cannot reach each other"
	}
	if n.Internal {
		message += "; internal: its instances cannot reach the host or beyond it"
	}
	h.record(n, events.ActionCreated, message)

	return networkToProto(n, 0), nil
}

// networkGateway returns the requested gateway, or the subnet's first
// address.
func networkGateway(subnet *net.IPNet, want string) (string, error) {
	if want == "" {
		first := slices.Clone(subnet.IP.To4())
		first[len(first)-1]++
		return first.String(), nil
	}

	gateway := net.ParseIP(want)
	if gateway == nil || !network.Assignable(subnet, gateway) {
		return "", errdefs.InvalidArgument(
			"gateway %q is not an assignable address in subnet %s", want, subnet)
	}
	return gateway.To4().String(), nil
}

// The MTU range a network may have.
const (
	minMTU = 576
	maxMTU = 9000
)

// networkMTU returns the requested MTU, or the default.
func networkMTU(want int32) (int, error) {
	if want == 0 {
		return network.DefaultMTU, nil
	}
	if want < minMTU || want > maxMTU {
		return 0, errdefs.InvalidArgument("MTU %d is out of range: want %d to %d", want, minMTU, maxMTU)
	}
	return int(want), nil
}

// networkNameservers returns the requested nameservers, or the default.
func networkNameservers(want []string) ([]string, error) {
	if len(want) == 0 {
		return []string{network.DefaultNameserver}, nil
	}
	for _, nameserver := range want {
		if net.ParseIP(nameserver) == nil {
			return nil, errdefs.InvalidArgument("nameserver %q is not an IP address", nameserver)
		}
	}
	return want, nil
}

// checkSubnetOverlap rejects a subnet that overlaps an existing network, or
// one the host is on, whose addresses the network's would hide.
func (h *networkHandler) checkSubnetOverlap(want *net.IPNet) error {
	for _, existing := range h.definitions.Networks() {
		_, have, err := net.ParseCIDR(existing.Subnet)
		if err != nil {
			continue
		}
		if have.Contains(want.IP) || want.Contains(have.IP) {
			return errdefs.Exists(
				"subnet %s overlaps network %q (%s)", want, existing.Name, existing.Subnet)
		}
	}

	if h.hostSubnets == nil {
		return nil
	}
	hostSubnets, err := h.hostSubnets()
	if err != nil {
		return fmt.Errorf("list the host's subnets: %w", err)
	}
	ones, _ := want.Mask.Size()
	wantPrefix := netip.PrefixFrom(netip.AddrFrom4([4]byte(want.IP.To4())), ones)
	for _, p := range hostSubnets {
		if p.Overlaps(wantPrefix) {
			return errdefs.Exists("subnet %s overlaps %s, which the host is on: choose another", want, p)
		}
	}

	return nil
}

// ListNetworks lists the networks with their address usage, sorted by name.
func (h *networkHandler) ListNetworks(
	_ context.Context, _ *dicerdv1.ListNetworksRequest,
) (*dicerdv1.ListNetworksResponse, error) {
	networks := h.definitions.Networks()

	resp := &dicerdv1.ListNetworksResponse{
		Networks: make([]*dicerdv1.Network, 0, len(networks)),
	}
	for _, n := range networks {
		allocations, err := h.networks.List(n.Name)
		if err != nil {
			return nil, err
		}
		resp.Networks = append(resp.Networks, networkToProto(n, len(allocations)))
	}

	return resp, nil
}

// GetNetwork returns a network with its address usage.
func (h *networkHandler) GetNetwork(
	_ context.Context, req *dicerdv1.GetNetworkRequest,
) (*dicerdv1.Network, error) {
	n, err := h.definitions.Network(req.GetName())
	if err != nil {
		return nil, err
	}

	allocations, err := h.networks.List(n.Name)
	if err != nil {
		return nil, err
	}

	return networkToProto(n, len(allocations)), nil
}

// DeleteNetwork removes a network and its allocations, refusing one an
// instance is on.
func (h *networkHandler) DeleteNetwork(
	_ context.Context, req *dicerdv1.DeleteNetworkRequest,
) (*emptypb.Empty, error) {
	n, err := h.definitions.Network(req.GetName())
	if err != nil {
		return nil, err
	}

	inUse := func(instance types.InstanceSpec) bool { return instance.NetworkName == n.Name }
	if err := refuseInUse(h.definitions, fmt.Sprintf("network %q is in use", n.Name), inUse); err != nil {
		return nil, err
	}

	if err := h.definitions.DeleteNetwork(n.Name); err != nil {
		return nil, err
	}
	h.record(n, events.ActionDeleted, "Deleted network with subnet "+n.Subnet)

	if err := h.networks.Forget(n.Name); err != nil {
		return nil, fmt.Errorf("discard allocations: %w", err)
	}

	return &emptypb.Empty{}, nil
}

// record records that action happened to n, with its subnet and gateway
// among the attributes.
func (h *networkHandler) record(n types.Network, action events.Action, message string) {
	h.events.Record(events.Event{
		Kind:       events.KindNetwork,
		ID:         n.ID,
		Name:       n.Name,
		Action:     action,
		Message:    message,
		Attributes: map[string]string{"subnet": n.Subnet, "gateway": n.Gateway},
	})
}

// ListNetworkAllocations lists the addresses a network has allocated.
func (h *networkHandler) ListNetworkAllocations(
	_ context.Context, req *dicerdv1.ListNetworkAllocationsRequest,
) (*dicerdv1.ListNetworkAllocationsResponse, error) {
	n, err := h.definitions.Network(req.GetName())
	if err != nil {
		return nil, err
	}

	allocations, err := h.networks.List(n.Name)
	if err != nil {
		return nil, err
	}

	instances := h.definitions.Instances()
	nameByID := make(map[string]string, len(instances))
	for _, instance := range instances {
		nameByID[instance.ID] = instance.Name
	}

	resp := &dicerdv1.ListNetworkAllocationsResponse{
		Allocations: make([]*dicerdv1.NetworkAllocation, 0, len(allocations)),
	}
	for _, a := range allocations {
		resp.Allocations = append(resp.Allocations, allocationToProto(a, nameByID[a.InstanceID]))
	}

	return resp, nil
}
