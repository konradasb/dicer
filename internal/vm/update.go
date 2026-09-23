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

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/image/reference"
)

// Update replaces a stopped instance's definition with updated, which must
// have the same ID. Editing a running instance is refused: the change could
// not take effect until a restart, and a definition silently diverging from
// what is running is worse than saying no.
//
// The restart policy is the exception. It is read when the instance ends,
// not when it starts, so a change to it alone takes effect at once and is
// accepted whatever the instance is doing.
//
// An instance moved to another network, or given another static IP, gives up
// the address it held, so that its next start allocates the one it now asks
// for.
func (m *Manager) Update(ctx context.Context, updated dicer.InstanceSpec) error {
	lock := m.lock(updated.ID)
	lock.Lock()
	defer lock.Unlock()

	current, err := m.definitions.GetInstance(updated.ID)
	if err != nil {
		return err
	}
	rt, err := m.Runtime(current)
	if err != nil {
		return err
	}
	if rt.State != dicer.StateStopped && !onlyRestartPolicyDiffers(current, updated) {
		return dicer.InvalidState("instance %q is %s; stop it before changing it", current.Name, rt.State.Lower())
	}

	if err := m.definitions.UpdateInstance(updated); err != nil {
		return fmt.Errorf("update instance %q: %w", current.Name, err)
	}
	m.record(updated, dicer.ActionUpdated, updateMessage(current, updated, rt.State), nil)

	if current.NetworkName != updated.NetworkName || current.StaticIP != updated.StaticIP {
		if err := m.addresses.Release(current.NetworkName, current.ID); err != nil {
			m.logger.WarnContext(ctx, "failed to release the address of a moved instance",
				"instance", current.Name, "network", current.NetworkName, "error", err)
		}
	}

	return nil
}

// updateMessage describes an update from current to updated of an
// instance in state: what changed, and when it takes effect.
func updateMessage(current, updated dicer.InstanceSpec, state dicer.InstanceState) string {
	changed := definitionChanges(current, updated)
	if len(changed) == 0 {
		return "Updated instance: nothing changed"
	}
	when := "takes effect on next start"
	if state != dicer.StateStopped {
		when = "takes effect when the instance next ends"
	}
	return "Updated instance: " + strings.Join(changed, ", ") + "; " + when
}

// definitionChanges lists what differs between two definitions of an
// instance, as "memory 256 MiB → 512 MiB" or, for what is too long to show,
// "environment changed".
func definitionChanges(a, b dicer.InstanceSpec) []string {
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

	from("image", reference.Familiar(a.ImageRef), reference.Familiar(b.ImageRef))
	from("vCPUs", strconv.Itoa(a.VCPUs), strconv.Itoa(b.VCPUs))
	from("memory", size(a.MemoryBytes), size(b.MemoryBytes))
	from("disk", size(a.DiskBytes), size(b.DiskBytes))
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
	changed("volumes", slices.Equal(a.VolumeMounts, b.VolumeMounts))
	changed("files", slices.Equal(a.Files, b.Files))
	changed("labels", maps.Equal(a.Labels, b.Labels))
	return out
}

// healthCheckString is an instance's own health check, as an update shows
// it: none is the image's.
func healthCheckString(c *dicer.HealthCheck) string {
	if c == nil {
		return "the image's"
	}
	return c.String()
}

// onlyRestartPolicyDiffers reports whether b is a, at most with another
// restart policy. When each was last updated does not count.
func onlyRestartPolicyDiffers(a, b dicer.InstanceSpec) bool {
	a.Restart, b.Restart = dicer.RestartPolicy{}, dicer.RestartPolicy{}
	a.UpdatedAt = b.UpdatedAt
	return reflect.DeepEqual(a, b)
}
