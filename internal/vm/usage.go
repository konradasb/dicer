// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"github.com/dicer-sh/dicer"
)

// Usage returns what the instances on this host hold at one moment, against
// what the host can give them.
//
// It is computed on demand from the definitions and their runtime state
// rather than kept as a running total, because the daemon is not the only
// thing that changes what is running: a VMM can die on its own, and recovery
// re-adopts whatever it finds. A total maintained alongside the truth would
// eventually disagree with it. Admission sums the same way; see resources.go.
// A definition whose runtime state cannot be read is counted as Failed, which
// is how the rest of this package treats an unreadable one too.
func (m *Manager) Usage() dicer.Usage {
	usage := dicer.Usage{
		ByState:  make(map[dicer.InstanceState]int, len(dicer.InstanceStates())),
		ByHealth: make(map[dicer.HealthStatus]int, len(dicer.HealthStatuses())),
		Capacity: m.capacity,
	}
	for _, state := range dicer.InstanceStates() {
		usage.ByState[state] = 0
	}
	for _, status := range dicer.HealthStatuses() {
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
			rt = dicer.InstanceStatus{State: dicer.StateFailed}
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
		usage.Holders = append(usage.Holders, dicer.Holder{Name: inst.Name, State: rt.State, Resources: held})
	}

	return usage
}
