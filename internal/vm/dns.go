// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"net/netip"
	"strings"

	"github.com/konradasb/dicer/internal/types"
)

// The Manager is what a network's DNS server asks about the network: see
// package dns.

// LookupHost returns the addresses of the instances on a network whose name
// or hostname is name, compared without regard to case. Only instances that
// are running, or starting, are found: a stopped one's address answers
// nothing.
func (m *Manager) LookupHost(network, name string) []netip.Addr {
	found := m.answerableInstances(network, func(inst types.InstanceSpec) bool {
		return strings.EqualFold(inst.Name, name) || strings.EqualFold(inst.Hostname, name)
	})
	out := make([]netip.Addr, 0, len(found))
	for _, inst := range found {
		out = append(out, inst.addr)
	}
	return out
}

// LookupAddr returns the name of the running instance on a network that
// holds addr, and its hostname if it is another.
func (m *Manager) LookupAddr(network string, addr netip.Addr) []string {
	instanceID, ok := m.networks.InstanceAt(network, addr.String())
	if !ok {
		return nil
	}

	found := m.answerableInstances(network, func(inst types.InstanceSpec) bool { return inst.ID == instanceID })
	if len(found) == 0 {
		return nil
	}
	spec := found[0].spec
	names := []string{spec.Name}
	if h := spec.Hostname; h != "" && !strings.EqualFold(h, spec.Name) {
		names = append(names, h)
	}
	return names
}

// answerableInstance is an instance a network's DNS server may give out.
type answerableInstance struct {
	spec types.InstanceSpec
	addr netip.Addr
}

// answerableInstances returns the instances on a network that match and
// are running or starting, with their addresses. Matching comes first, as
// every query asks, most of them about names no instance has, and an
// instance's state is read from disk.
func (m *Manager) answerableInstances(network string, matches func(types.InstanceSpec) bool) []answerableInstance {
	instances := m.definitions.MatchingInstances(func(inst types.InstanceSpec) bool {
		return inst.NetworkName == network && matches(inst)
	})

	var out []answerableInstance
	for _, inst := range instances {
		rt, err := m.Runtime(inst)
		if err != nil || (!rt.State.IsActive() && rt.State != types.StateStarting) {
			continue
		}
		alloc, err := m.networks.Get(network, inst.ID)
		if err != nil {
			continue
		}
		addr, err := netip.ParseAddr(alloc.IP)
		if err != nil {
			continue
		}
		out = append(out, answerableInstance{spec: inst, addr: addr})
	}
	return out
}
