// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/types"
)

// Update replaces a stopped instance's definition. A change to its restart
// policy or standby_after alone is accepted in any state. Changing the network or static IP
// releases the instance's address. A larger disk_bytes grows the overlay
// disk at the next start; a smaller one than the overlay disk is refused.
func (m *Manager) Update(ctx context.Context, updated types.InstanceSpec) error {
	lock := m.lock(updated.ID)
	lock.Lock()
	defer lock.Unlock()
	defer m.syncWaker(ctx, updated.ID)

	current, err := m.definitions.Instance(updated.ID)
	if err != nil {
		return err
	}
	status, err := m.Status(current)
	if err != nil {
		return err
	}
	if err := m.checkCanUpdate(current, updated, status.State); err != nil {
		return err
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

// checkCanUpdate returns an error unless current, in state, can be changed
// to updated: only its restart policy and standby_after can change while it
// is not stopped, and its overlay disk cannot shrink.
func (m *Manager) checkCanUpdate(current, updated types.InstanceSpec, state types.InstanceState) error {
	if state != types.InstanceStateStopped && !onlyPoliciesDiffer(current, updated) {
		return errdefs.InvalidState("instance %q is %s; stop it before changing it", current.Name, state.Lowercase())
	}
	if updated.DiskBytes == current.DiskBytes {
		return nil
	}

	// The overlay disk grows to disk_bytes at the next start. Before the
	// first, there is none to shrink.
	info, err := os.Stat(m.overlayDiskPath(current))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read overlay disk size: %w", err)
	}
	if updated.DiskBytes < info.Size() {
		return errdefs.InvalidArgument("instance %q has a %s overlay disk, which cannot shrink: "+
			"give it at least %[2]s, or recreate the instance for a smaller disk",
			current.Name, humanize.Bytes(info.Size()))
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
		when = "takes effect at once"
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
	// limit is a rate limit as written, of which zero is unlimited.
	limit := func(n int64, written string) string {
		if n == 0 {
			return "unlimited"
		}
		return written
	}
	byteRate := func(n int64) string { return limit(n, humanize.Bytes(n)+"/s") }
	iops := func(n int64) string { return limit(n, strconv.FormatInt(n, 10)) }

	from("image", reference.FamiliarString(a.ImageRef), reference.FamiliarString(b.ImageRef))
	from("vCPUs", strconv.Itoa(a.VCPUs), strconv.Itoa(b.VCPUs))
	from("memory", humanize.Bytes(a.MemoryBytes), humanize.Bytes(b.MemoryBytes))
	from("max vCPUs", maximum(int64(a.MaxVCPUs), strconv.Itoa(a.MaxVCPUs)), maximum(int64(b.MaxVCPUs), strconv.Itoa(b.MaxVCPUs)))
	from("max memory", maximum(a.MaxMemoryBytes, humanize.Bytes(a.MaxMemoryBytes)), maximum(b.MaxMemoryBytes, humanize.Bytes(b.MaxMemoryBytes)))
	from("disk", humanize.Bytes(a.DiskBytes), humanize.Bytes(b.DiskBytes))
	from("disk rate", byteRate(a.DiskBytesPerSecond), byteRate(b.DiskBytesPerSecond))
	from("disk IOPS", iops(a.DiskIOPS), iops(b.DiskIOPS))
	from("standby after", standbyAfter(a.StandbyAfter), standbyAfter(b.StandbyAfter))
	from("upload rate", byteRate(a.UploadBytesPerSecond), byteRate(b.UploadBytesPerSecond))
	from("download rate", byteRate(a.DownloadBytesPerSecond), byteRate(b.DownloadBytesPerSecond))
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
	changed("mounts", slices.EqualFunc(a.Mounts, b.Mounts, types.Mount.Equal))
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

// standbyAfter describes how long an instance may be idle before standby,
// as an update lists it: "15m", or "never".
func standbyAfter(d time.Duration) string {
	if d == 0 {
		return "never"
	}
	return humanize.Duration(d)
}

// onlyPoliciesDiffer reports whether a and b differ at most in what the
// daemon decides by rather than what the guest runs with, which can change
// in any state: their restart policy, when they go on standby, and
// UpdatedAt.
func onlyPoliciesDiffer(a, b types.InstanceSpec) bool {
	a.Restart, b.Restart = types.RestartPolicy{}, types.RestartPolicy{}
	a.StandbyAfter, b.StandbyAfter = 0, 0
	a.UpdatedAt = b.UpdatedAt
	return reflect.DeepEqual(a, b)
}
