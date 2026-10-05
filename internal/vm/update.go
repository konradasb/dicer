// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/types"
)

// Update replaces a stopped instance's definition. A change to the restart
// policy alone is accepted in any state. Changing the network or static IP
// releases the instance's address.
func (m *Manager) Update(ctx context.Context, updated types.InstanceSpec) error {
	lock := m.lock(updated.ID)
	lock.Lock()
	defer lock.Unlock()

	current, err := m.definitions.Instance(updated.ID)
	if err != nil {
		return err
	}
	status, err := m.Status(current)
	if err != nil {
		return err
	}
	if status.State != types.InstanceStateStopped && !onlyRestartPolicyDiffers(current, updated) {
		return errdefs.InvalidState("instance %q is %s; stop it before changing it", current.Name, status.State.Lowercase())
	}

	if err := m.definitions.UpdateInstance(updated); err != nil {
		return fmt.Errorf("update instance %q: %w", current.Name, err)
	}
	m.record(updated, events.ActionUpdated, updateMessage(current, updated, status.State), nil)

	if current.NetworkName != updated.NetworkName || current.StaticIP != updated.StaticIP {
		if err := m.networks.Release(current.NetworkName, current.ID); err != nil {
			m.logger.WarnContext(ctx, "failed to release the address of a moved instance",
				"instance", current.Name, "network", current.NetworkName, "error", err)
		}
	}

	return nil
}

// updateMessage describes what an update changed and when it takes effect.
func updateMessage(current, updated types.InstanceSpec, state types.InstanceState) string {
	changed := definitionChanges(current, updated)
	if len(changed) == 0 {
		return "Updated instance: nothing changed"
	}
	when := "takes effect on next start"
	if state != types.InstanceStateStopped {
		when = "takes effect when the instance next ends"
	}
	return "Updated instance: " + strings.Join(changed, ", ") + "; " + when
}

// definitionChanges lists what differs between two definitions:
// "memory 256 MiB → 512 MiB", "environment changed".
func definitionChanges(a, b types.InstanceSpec) []string {
	var out []string
	from := func(name, x, y string) {
		if x != y {
			out = append(out, fmt.Sprintf("%s %s → %s", name, cmp.Or(x, "none"), cmp.Or(y, "none")))
		}
	}
	changed := func(name string, same bool) {
		if !same {
			out = append(out, name+" changed")
		}
	}
	// maximum is a maximum as written, of which zero is none.
	maximum := func(n int64, written string) string {
		if n == 0 {
			return ""
		}
		return written
	}

	from("image", reference.FamiliarString(a.ImageRef), reference.FamiliarString(b.ImageRef))
	from("vCPUs", strconv.Itoa(a.VCPUs), strconv.Itoa(b.VCPUs))
	from("memory", humanize.Bytes(a.MemoryBytes), humanize.Bytes(b.MemoryBytes))
	from("max vCPUs", maximum(int64(a.MaxVCPUs), strconv.Itoa(a.MaxVCPUs)), maximum(int64(b.MaxVCPUs), strconv.Itoa(b.MaxVCPUs)))
	from("max memory", maximum(a.MaxMemoryBytes, humanize.Bytes(a.MaxMemoryBytes)), maximum(b.MaxMemoryBytes, humanize.Bytes(b.MaxMemoryBytes)))
	from("disk", humanize.Bytes(a.DiskBytes), humanize.Bytes(b.DiskBytes))
	from("restart policy", a.Restart.String(), b.Restart.String())
	from("network", a.NetworkName, b.NetworkName)
	from("static IP", a.StaticIP, b.StaticIP)
	from("hostname", a.Hostname, b.Hostname)
	from("hypervisor", strings.TrimSpace(string(a.HypervisorType)+" "+a.HypervisorVersion),
		strings.TrimSpace(string(b.HypervisorType)+" "+b.HypervisorVersion))
	from("kernel", a.KernelName, b.KernelName)
	from("kernel args", a.KernelArgs, b.KernelArgs)
	from("init mode", string(a.InitMode), string(b.InitMode))
	from("health check", healthCheckString(a.HealthCheck), healthCheckString(b.HealthCheck))
	changed("command", slices.Equal(a.Cmd, b.Cmd))
	changed("environment", maps.Equal(a.Env, b.Env))
	changed("ports", slices.Equal(a.Ports, b.Ports))
	changed("mounts", slices.Equal(a.Mounts, b.Mounts))
	changed("labels", maps.Equal(a.Labels, b.Labels))
	return out
}

// healthCheckString describes an instance's health check; nil is the
// image's.
func healthCheckString(c *types.HealthCheck) string {
	if c == nil {
		return "the image's"
	}
	return c.String()
}

// onlyRestartPolicyDiffers reports whether a and b differ at most in their
// restart policy and UpdatedAt.
func onlyRestartPolicyDiffers(a, b types.InstanceSpec) bool {
	a.Restart, b.Restart = types.RestartPolicy{}, types.RestartPolicy{}
	a.UpdatedAt = b.UpdatedAt
	return reflect.DeepEqual(a, b)
}
