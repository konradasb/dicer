// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import "github.com/konradasb/dicer/internal/types"

// Usage returns what the instances on this host hold now. An instance whose
// status cannot be read counts as Failed.
func (m *Manager) Usage() types.Usage {
	usage := types.Usage{
		ByState:  make(map[types.InstanceState]int, len(types.InstanceStates())),
		ByHealth: make(map[types.HealthStatus]int, len(types.HealthStatuses())),
		Capacity: m.capacity,
	}
	for _, state := range types.InstanceStates() {
		usage.ByState[state] = 0
	}
	for _, healthStatus := range types.HealthStatuses() {
		usage.ByHealth[healthStatus] = 0
	}

	instances := m.definitions.Instances()

	for _, instance := range instances {
		status, err := m.Status(instance)
		if err != nil {
			status = types.InstanceStatus{State: types.InstanceStateFailed}
		}

		usage.ByState[status.State]++
		if _, health, ok := m.Health(instance); ok {
			usage.ByHealth[health.Status]++
		}

		if !status.State.HoldsResources() {
			continue
		}
		held := status.HeldResources()
		usage.Allocated = usage.Allocated.Add(held)
		usage.Instances = append(usage.Instances, types.InstanceResources{Name: instance.Name, State: status.State, Resources: held})
	}

	return usage
}
