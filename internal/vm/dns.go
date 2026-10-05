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
	found := m.answerableInstances(network, func(instance types.InstanceSpec) bool {
		return strings.EqualFold(instance.Name, name) || strings.EqualFold(instance.Hostname, name)
	})
	out := make([]netip.Addr, 0, len(found))
	for _, instance := range found {
		out = append(out, instance.addr)
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

	found := m.answerableInstances(network, func(instance types.InstanceSpec) bool { return instance.ID == instanceID })
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
	instances := m.definitions.MatchingInstances(func(instance types.InstanceSpec) bool {
		return instance.NetworkName == network && matches(instance)
	})

	var out []answerableInstance
	for _, instance := range instances {
		status, err := m.Status(instance)
		if err != nil || (!status.State.IsActive() && status.State != types.InstanceStateStarting) {
			continue
		}
		allocation, err := m.networks.Allocation(network, instance.ID)
		if err != nil {
			continue
		}
		addr, err := netip.ParseAddr(allocation.IP)
		if err != nil {
			continue
		}
		out = append(out, answerableInstance{spec: instance, addr: addr})
	}
	return out
}
