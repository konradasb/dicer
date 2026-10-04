// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"slices"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
)

// Address returns the address assigned to an instance, if it holds one.
func (m *Manager) Address(inst types.InstanceSpec) (types.NetworkAllocation, error) {
	return m.networks.Get(inst.NetworkName, inst.ID)
}

// networkSetup holds the result of attaching an instance to its network.
type networkSetup struct {
	nic         hypervisor.NetworkInterfaceConfig
	gateway     string
	nameservers []string
	prefixLen   int
	cleanup     func()
}

// setupNetwork allocates an address and attaches a TAP device to the
// network's bridge.
func (m *Manager) setupNetwork(ctx context.Context, inst types.InstanceSpec) (*networkSetup, error) {
	nw, err := m.definitions.GetNetwork(inst.NetworkName)
	if err != nil {
		return nil, fmt.Errorf("get network %q: %w", inst.NetworkName, err)
	}

	alloc, err := m.networks.Allocate(nw, inst.ID, inst.StaticIP)
	if err != nil {
		return nil, fmt.Errorf("allocate address on network %q: %w", inst.NetworkName, err)
	}

	// The address is kept on failure; only host devices are undone. The undo
	// must run even if ctx was cancelled.
	undo := func() {
		ctx := context.WithoutCancel(ctx)
		m.hostNetwork.UnpublishPorts(ctx, inst.ID)
		m.hostNetwork.RemoveTAP(ctx, &nw, inst.ID)
	}

	servesDNS, err := m.attachTAP(ctx, &nw, &alloc)
	if err != nil {
		return nil, err
	}

	if len(inst.Ports) > 0 {
		if err := m.hostNetwork.PublishPorts(ctx, &nw, &alloc, inst.Ports); err != nil {
			undo()
			return nil, err
		}
	}

	netmask, err := nw.Netmask()
	if err != nil {
		undo()
		return nil, err
	}
	prefixLen, err := nw.PrefixLen()
	if err != nil {
		undo()
		return nil, err
	}

	mtu := nw.MTU
	if mtu == 0 {
		mtu = network.DefaultMTU
	}

	// The gateway, which answers for the network's instances and asks the
	// upstream nameservers the rest; or, if it cannot, those nameservers.
	nameservers := []string{nw.Gateway}
	if !servesDNS {
		nameservers = upstreamNameservers(nw)
	}

	return &networkSetup{
		nic: hypervisor.NetworkInterfaceConfig{
			TapDevice: network.TAPName(inst.ID),
			IP:        alloc.IP,
			MAC:       alloc.MAC,
			Netmask:   netmask,
			MTU:       mtu,
		},
		gateway:     nw.Gateway,
		nameservers: nameservers,
		prefixLen:   prefixLen,
		cleanup:     undo,
	}, nil
}

// upstreamNameservers are the nameservers a network's names are looked up
// in: its own, or the default.
func upstreamNameservers(nw types.Network) []string {
	if len(nw.Nameservers) > 0 {
		return nw.Nameservers
	}
	return []string{network.DefaultNameserver}
}

// attachTAP brings the network's bridge up, with its DNS server, and
// attaches the instance's TAP device to it, under the network lock. It
// reports whether the network's DNS server is serving.
func (m *Manager) attachTAP(ctx context.Context, nw *types.Network, alloc *types.NetworkAllocation) (bool, error) {
	lock := m.networkLock(nw.Name)
	lock.Lock()
	defer lock.Unlock()

	if err := m.hostNetwork.SetupBridge(ctx, nw); err != nil {
		return false, fmt.Errorf("set up bridge %q: %w", nw.Bridge, err)
	}
	if err := m.hostNetwork.CreateTAP(ctx, nw, alloc, network.Bandwidth{}); err != nil {
		m.hostNetwork.RemoveTAP(context.WithoutCancel(ctx), nw, alloc.InstanceID)
		return false, fmt.Errorf("create TAP device: %w", err)
	}
	return m.serveDNS(ctx, *nw), nil
}

// serveDNS starts the network's DNS server, unless it is serving, and
// reports whether it is. One that cannot start costs the guests their
// neighbours' names, not their DNS: they are given the upstream
// nameservers instead.
func (m *Manager) serveDNS(ctx context.Context, nw types.Network) bool {
	if m.dnsServers == nil {
		return false
	}
	if err := m.dnsServers.Serve(ctx, nw); err != nil {
		m.logger.WarnContext(ctx, "cannot serve DNS on the network; its instances get its upstream nameservers",
			"network", nw.Name, "error", err)
		return false
	}
	return true
}

// teardownNetwork removes an instance's published ports and TAP device, and
// the network's bridge if no other instance uses it. The address is kept.
// Failures are logged.
func (m *Manager) teardownNetwork(ctx context.Context, inst types.InstanceSpec) {
	nw, err := m.definitions.GetNetwork(inst.NetworkName)
	if err != nil {
		m.logger.WarnContext(ctx, "network not found while tearing it down",
			"instance", inst.Name, "network", inst.NetworkName, "error", err)
		return
	}

	m.hostNetwork.UnpublishPorts(ctx, inst.ID)
	m.hostNetwork.RemoveTAP(ctx, &nw, inst.ID)

	lock := m.networkLock(nw.Name)
	lock.Lock()
	defer lock.Unlock()

	if m.networkInUse(nw, inst.ID) {
		return
	}

	m.logger.InfoContext(ctx, "tearing down bridge, no instances left on network",
		"network", nw.Name, "bridge", nw.Bridge)
	if m.dnsServers != nil {
		m.dnsServers.Stop(nw.Name)
	}
	m.hostNetwork.TeardownBridge(ctx, &nw)
}

// networkInUse reports whether any instance other than excludeID is active
// on the network. It errs towards true.
func (m *Manager) networkInUse(nw types.Network, excludeID string) bool {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		return true
	}

	for _, other := range instances {
		if other.ID == excludeID || other.NetworkName != nw.Name {
			continue
		}
		rt, err := m.Runtime(other)
		if err != nil {
			return true
		}
		if rt.State.IsActive() || rt.State == types.StateStarting {
			return true
		}
	}

	return false
}

// checkPorts refuses an instance that would publish a host port another
// active instance holds. The caller must hold admissionMu.
func (m *Manager) checkPorts(inst types.InstanceSpec) error {
	if len(inst.Ports) == 0 {
		return nil
	}

	instances, err := m.definitions.ListInstances()
	if err != nil {
		return err
	}

	for _, other := range instances {
		if other.ID == inst.ID || len(other.Ports) == 0 {
			continue
		}

		rt, err := m.Runtime(other)
		if err != nil {
			return err
		}
		if !rt.State.HoldsResources() && rt.State != types.StateStopping {
			continue
		}

		for _, p := range inst.Ports {
			if slices.ContainsFunc(other.Ports, p.Overlaps) {
				return errdefs.InvalidState("port %s is already published by instance %q, which is %s",
					p, other.Name, rt.State.Lower())
			}
		}
	}

	return nil
}
