// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/nrednav/cuid2"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// networkHandler handles network-related RPCs.
type networkHandler struct {
	definitions *filestore.Manager
	addresses   *network.Manager
}

func (h *networkHandler) CreateNetwork(
	_ context.Context, req *dicerdv1.CreateNetworkRequest,
) (*dicerdv1.Network, error) {
	if err := naming.Validate(req.GetName()); err != nil {
		return nil, err
	}
	if req.GetSubnet() == "" {
		return nil, errdefs.InvalidArgument("subnet is required")
	}

	if _, err := h.definitions.GetNetwork(req.GetName()); err == nil {
		return nil, errdefs.Exists("network %q already exists", req.GetName())
	}

	ipNet, err := network.ParseSubnet(req.GetSubnet())
	if err != nil {
		return nil, err
	}
	gateway, err := networkGateway(ipNet, req.GetGateway())
	if err != nil {
		return nil, err
	}
	if err := h.checkSubnetOverlap(ipNet); err != nil {
		return nil, err
	}
	mtu, err := networkMTU(req.GetMtu())
	if err != nil {
		return nil, err
	}
	nameservers, err := networkNameservers(req.GetNameservers())
	if err != nil {
		return nil, err
	}

	now := time.Now()
	n := types.Network{
		ID:          cuid2.Generate(),
		Name:        req.GetName(),
		Subnet:      ipNet.String(),
		Gateway:     gateway,
		Bridge:      network.BridgeName(req.GetName()),
		MTU:         mtu,
		Nameservers: nameservers,
		Isolated:    req.GetIsolated(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := h.definitions.CreateNetwork(n); err != nil {
		return nil, err
	}

	return networkToProto(n, 0), nil
}

// networkGateway returns the requested gateway, or the subnet's first
// address.
func networkGateway(ipNet *net.IPNet, want string) (string, error) {
	if want == "" {
		first := slices.Clone(ipNet.IP.To4())
		first[len(first)-1]++
		return first.String(), nil
	}

	gateway := net.ParseIP(want)
	if gateway == nil || !network.Assignable(ipNet, gateway) {
		return "", errdefs.InvalidArgument(
			"gateway %q is not an assignable address in subnet %s", want, ipNet)
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
	for _, ns := range want {
		if net.ParseIP(ns) == nil {
			return nil, errdefs.InvalidArgument("nameserver %q is not an IP address", ns)
		}
	}
	return want, nil
}

// checkSubnetOverlap rejects a subnet that overlaps an existing network.
func (h *networkHandler) checkSubnetOverlap(want *net.IPNet) error {
	networks, err := h.definitions.ListNetworks()
	if err != nil {
		return err
	}

	for _, existing := range networks {
		_, have, err := net.ParseCIDR(existing.Subnet)
		if err != nil {
			continue
		}
		if have.Contains(want.IP) || want.Contains(have.IP) {
			return errdefs.InvalidArgument(
				"subnet %s overlaps network %q (%s)", want, existing.Name, existing.Subnet)
		}
	}

	return nil
}

func (h *networkHandler) ListNetworks(
	_ context.Context, _ *dicerdv1.ListNetworksRequest,
) (*dicerdv1.ListNetworksResponse, error) {
	networks, err := h.definitions.ListNetworks()
	if err != nil {
		return nil, err
	}

	resp := &dicerdv1.ListNetworksResponse{
		Networks: make([]*dicerdv1.Network, 0, len(networks)),
	}
	for _, n := range networks {
		allocs, err := h.addresses.List(n.Name)
		if err != nil {
			return nil, err
		}
		resp.Networks = append(resp.Networks, networkToProto(n, len(allocs)))
	}

	return resp, nil
}

func (h *networkHandler) GetNetwork(
	_ context.Context, req *dicerdv1.GetNetworkRequest,
) (*dicerdv1.Network, error) {
	n, err := h.definitions.GetNetwork(req.GetName())
	if err != nil {
		return nil, err
	}

	allocs, err := h.addresses.List(n.Name)
	if err != nil {
		return nil, err
	}

	return networkToProto(n, len(allocs)), nil
}

func (h *networkHandler) DeleteNetwork(
	_ context.Context, req *dicerdv1.DeleteNetworkRequest,
) (*emptypb.Empty, error) {
	n, err := h.definitions.GetNetwork(req.GetName())
	if err != nil {
		return nil, err
	}

	inUse := func(inst types.InstanceSpec) bool { return inst.NetworkName == n.Name }
	if err := refuseInUse(h.definitions, fmt.Sprintf("network %q is in use", n.Name), inUse); err != nil {
		return nil, err
	}

	if err := h.definitions.DeleteNetwork(n.Name); err != nil {
		return nil, err
	}

	// Drop the network's address table.
	if err := h.addresses.Forget(n.Name); err != nil {
		return nil, fmt.Errorf("discard allocations: %w", err)
	}

	return &emptypb.Empty{}, nil
}

func (h *networkHandler) ListNetworkAllocations(
	_ context.Context, req *dicerdv1.ListNetworkAllocationsRequest,
) (*dicerdv1.ListNetworkAllocationsResponse, error) {
	n, err := h.definitions.GetNetwork(req.GetName())
	if err != nil {
		return nil, err
	}

	allocs, err := h.addresses.List(n.Name)
	if err != nil {
		return nil, err
	}

	instances, err := h.definitions.ListInstances()
	if err != nil {
		return nil, err
	}
	nameByID := make(map[string]string, len(instances))
	for _, inst := range instances {
		nameByID[inst.ID] = inst.Name
	}

	resp := &dicerdv1.ListNetworkAllocationsResponse{
		Allocations: make([]*dicerdv1.NetworkAllocation, 0, len(allocs)),
	}
	for _, a := range allocs {
		resp.Allocations = append(resp.Allocations, allocationToProto(a, nameByID[a.InstanceID]))
	}

	return resp, nil
}
