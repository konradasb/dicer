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
	return m.addresses.Get(inst.NetworkName, inst.ID)
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

	alloc, err := m.addresses.Allocate(nw, inst.ID, inst.StaticIP)
	if err != nil {
		return nil, fmt.Errorf("allocate address on network %q: %w", nw.Name, err)
	}

	// The address is kept on failure; only host devices are undone. The undo
	// must run even if ctx was cancelled.
	undo := func() {
		ctx := context.WithoutCancel(ctx)
		m.hostNetwork.UnpublishPorts(ctx, inst.ID)
		m.hostNetwork.RemoveTAP(ctx, &nw, inst.ID)
	}

	if err := m.attachTAP(ctx, &nw, &alloc); err != nil {
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

	nameservers := nw.Nameservers
	if len(nameservers) == 0 {
		nameservers = []string{network.DefaultNameserver}
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

// attachTAP brings the network's bridge up and attaches the instance's TAP
// device to it, under the network lock.
func (m *Manager) attachTAP(ctx context.Context, nw *types.Network, alloc *types.NetworkAllocation) error {
	lock := m.networkLock(nw.Name)
	lock.Lock()
	defer lock.Unlock()

	if err := m.hostNetwork.SetupBridge(ctx, nw); err != nil {
		return fmt.Errorf("set up bridge %q: %w", nw.Bridge, err)
	}
	if err := m.hostNetwork.CreateTAP(ctx, nw, alloc, network.Bandwidth{}); err != nil {
		m.hostNetwork.RemoveTAP(context.WithoutCancel(ctx), nw, alloc.InstanceID)
		return fmt.Errorf("create TAP device: %w", err)
	}
	return nil
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
