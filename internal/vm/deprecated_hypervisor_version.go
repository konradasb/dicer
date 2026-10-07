// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"

	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// WarnDeprecatedHypervisorVersions logs a warning for everything that still uses a
// deprecated hypervisor version, and must move off it before a release of
// Dicer removes the version: an instance that names one, a guest running or
// frozen on one, and a memory snapshot taken with one. The daemon calls it
// once, after Recover.
func (m *Manager) WarnDeprecatedHypervisorVersions(ctx context.Context) {
	for _, instance := range m.definitions.Instances() {
		hypervisorType := instance.EffectiveHypervisorType()
		starters := m.starters[hypervisorType]

		if hypervisor.IsDeprecated(starters, instance.HypervisorVersion) {
			m.logger.WarnContext(ctx, "instance names a deprecated hypervisor version",
				"instance", instance.Name, "hypervisor", hypervisorType, "hypervisor_version", instance.HypervisorVersion)
			continue
		}
		if version := m.guestHypervisorVersion(instance); hypervisor.IsDeprecated(starters, version) {
			m.logger.WarnContext(ctx, "instance's guest runs on a deprecated hypervisor version",
				"instance", instance.Name, "hypervisor", hypervisorType, "hypervisor_version", version)
		}
	}

	for _, snapshot := range m.definitions.Snapshots() {
		if snapshot.Kind != types.SnapshotKindMemory {
			continue
		}
		if hypervisor.IsDeprecated(m.starters[snapshot.HypervisorType], snapshot.HypervisorVersion) {
			m.logger.WarnContext(ctx, "memory snapshot was taken with a deprecated hypervisor version",
				"snapshot", snapshot.Name, "hypervisor", snapshot.HypervisorType, "hypervisor_version", snapshot.HypervisorVersion)
		}
	}
}

// guestHypervisorVersion returns the hypervisor version an instance's guest
// is running or frozen on, or "" if it has no guest or its version cannot be
// read.
func (m *Manager) guestHypervisorVersion(instance types.InstanceSpec) string {
	if status, err := m.Status(instance); err == nil && status.State.IsActive() {
		return status.HypervisorVersion
	}
	if !m.onStandby(instance) {
		return ""
	}
	standby, err := m.readStandby(instance)
	if err != nil {
		return ""
	}
	return standby.HypervisorVersion
}
