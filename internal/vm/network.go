// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"slices"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	"github.com/dicer-sh/dicer/internal/network"
)

// Address returns the address assigned to an instance, if it holds one.
func (m *Manager) Address(inst dicer.InstanceSpec) (dicer.NetworkAllocation, error) {
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
func (m *Manager) setupNetwork(ctx context.Context, inst dicer.InstanceSpec) (*networkSetup, error) {
	nw, err := m.definitions.GetNetwork(inst.NetworkName)
	if err != nil {
		return nil, fmt.Errorf("get network %q: %w", inst.NetworkName, err)
	}

	alloc, err := m.addresses.Allocate(nw, inst.ID, inst.StaticIP)
	if err != nil {
		return nil, fmt.Errorf("allocate address on network %q: %w", nw.Name, err)
	}

	// The allocation is persistent and survives a failed start, so that the
	// instance keeps its address; only the host-side devices are undone.
	// Deliberately not the caller's context: this undo runs precisely when
	// the start failed, which is often because that context was cancelled.
	// Cleanup has to outlive it.
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

// attachTAP brings the network's bridge up, if it is not already, and
// attaches the instance's TAP device to it. The network lock keeps the bridge
// from being torn down in between.
func (m *Manager) attachTAP(ctx context.Context, nw *dicer.Network, alloc *dicer.NetworkAllocation) error {
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

// teardownNetwork removes an instance's published ports and TAP device, and the network's bridge
// too if this was the last instance using it. The address allocation is
// kept: it belongs to the instance, not to the run. Failures are logged rather
// than returned: the caller is stopping or deleting an instance, and a
// leftover device must not prevent that from completing.
func (m *Manager) teardownNetwork(ctx context.Context, inst dicer.InstanceSpec) {
	nw, err := m.definitions.GetNetwork(inst.NetworkName)
	if err != nil {
		m.logger.WarnContext(ctx, "network not found while tearing it down",
			"instance", inst.Name, "network", inst.NetworkName, "error", err)
		return
	}

	// Unconditionally: the instance's ports are found by its ID, so this is
	// right whatever the definition says now.
	m.hostNetwork.UnpublishPorts(ctx, inst.ID)
	m.hostNetwork.RemoveTAP(ctx, &nw, inst.ID)

	// Held from the check to the teardown, so that an instance starting on
	// the network meanwhile cannot attach to a bridge about to go.
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

// networkInUse reports whether any instance other than the one given is
// currently running on the network.
func (m *Manager) networkInUse(nw dicer.Network, excludeID string) bool {
	instances, err := m.definitions.ListInstances()
	if err != nil {
		// If we cannot tell, keep the bridge. Leaving one up is harmless;
		// tearing it out from under a running VM is not.
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
		if rt.State.IsActive() || rt.State == dicer.StateStarting {
			return true
		}
	}

	return false
}

// checkPorts refuses an instance that would publish a host port another
// instance holds. Published ports hold no socket on the host, so this is the
// only place a clash between two instances can be seen.
//
// The caller must hold admissionMu, so that two starts cannot both pass.
func (m *Manager) checkPorts(inst dicer.InstanceSpec) error {
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
		// A stopping instance still has its ports until its network is
		// torn down.
		if !rt.State.HoldsResources() && rt.State != dicer.StateStopping {
			continue
		}

		for _, p := range inst.Ports {
			if slices.ContainsFunc(other.Ports, p.Overlaps) {
				return dicer.InvalidState("port %s is already published by instance %q, which is %s",
					p, other.Name, rt.State.Lower())
			}
		}
	}

	return nil
}
