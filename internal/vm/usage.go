// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import "github.com/konradasb/dicer/internal/types"

// Usage returns what the instances on this host hold now. An instance whose
// runtime state cannot be read counts as Failed.
func (m *Manager) Usage() types.Usage {
	usage := types.Usage{
		ByState:  make(map[types.InstanceState]int, len(types.InstanceStates())),
		ByHealth: make(map[types.HealthStatus]int, len(types.HealthStatuses())),
		Capacity: m.capacity,
	}
	for _, state := range types.InstanceStates() {
		usage.ByState[state] = 0
	}
	for _, status := range types.HealthStatuses() {
		usage.ByHealth[status] = 0
	}

	instances, err := m.definitions.ListInstances()
	if err != nil {
		m.logger.Warn("cannot list instances for usage", "error", err)
		return usage
	}

	for _, inst := range instances {
		rt, err := m.Runtime(inst)
		if err != nil {
			rt = types.InstanceStatus{State: types.StateFailed}
		}

		usage.ByState[rt.State]++
		if _, state, ok := m.Health(inst); ok {
			usage.ByHealth[state.Status]++
		}

		if !rt.State.HoldsResources() {
			continue
		}
		held := rt.Held()
		usage.Allocated = usage.Allocated.Add(held)
		usage.Holders = append(usage.Holders, types.Holder{Name: inst.Name, State: rt.State, Resources: held})
	}

	return usage
}
