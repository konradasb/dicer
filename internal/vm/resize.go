// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// resizeMemoryTimeout bounds how long Resize waits for the guest to take or
// give up memory.
const resizeMemoryTimeout = time.Minute

// Resize gives a running instance want's vCPUs and memory without
// restarting it, within its maximums, and records them in its definition so
// that it keeps them when it next starts. Growing needs room on the host
// (ErrResourceExhausted otherwise).
//
// The larger of what the instance held and want is reserved first. A size
// the hypervisor refuses changes nothing. If the hypervisor or the guest
// fails a resize it took, the instance keeps holding the larger until it
// next starts, with want.
func (m *Manager) Resize(ctx context.Context, instance types.InstanceSpec, want types.Resources) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationResize, started, err) }()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := m.Status(instance)
	if err != nil {
		return err
	}
	if status.State != types.InstanceStateRunning {
		return errdefs.InvalidState("instance %q is %s; only a running instance can be resized",
			instance.Name, status.State.Lowercase())
	}

	held := status.HeldResources()
	if err := checkResize(instance, held, want); err != nil {
		return err
	}
	if want == held {
		return nil
	}

	hv, err := m.connect(instance, status)
	if err != nil {
		return err
	}
	capabilities := hv.Capabilities()
	if want.VCPUs != held.VCPUs {
		if err := requireCapability(instance, capabilities.SupportsHotplugCPU, "resizing vCPUs"); err != nil {
			return err
		}
	}
	if want.MemoryBytes != held.MemoryBytes {
		if err := requireCapability(instance, capabilities.SupportsHotplugMemory, "resizing memory"); err != nil {
			return err
		}
	}

	larger := types.Resources{VCPUs: max(held.VCPUs, want.VCPUs), MemoryBytes: max(held.MemoryBytes, want.MemoryBytes)}
	if err := m.reserve(instance, larger); err != nil {
		return err
	}

	resizeErr := resizeVM(ctx, hv, held, want)
	if errors.Is(resizeErr, errdefs.ErrInvalidArgument) {
		// Refused before anything changed.
		if err := m.reserve(instance, held); err != nil {
			return errors.Join(resizeErr, err)
		}
		return resizeErr
	}

	// Kept even if the resize failed, so that the instance has want from its
	// next start.
	resized := instance
	resized.VCPUs, resized.MemoryBytes = want.VCPUs, want.MemoryBytes
	resized.UpdatedAt = time.Now()
	if err := m.definitions.UpdateInstance(resized); err != nil {
		return fmt.Errorf("update instance %q: %w", instance.Name, err)
	}
	if resizeErr != nil {
		return fmt.Errorf("resize instance %q: %w; restart it to give it %s", instance.Name, resizeErr, want)
	}

	if err := m.reserve(instance, want); err != nil {
		return err
	}

	m.record(resized, events.ActionResized,
		fmt.Sprintf("Resized instance from %s, %s memory to %s, %s memory in %s",
			humanize.Count(held.VCPUs, "vCPU"), humanize.Bytes(held.MemoryBytes),
			humanize.Count(want.VCPUs, "vCPU"), humanize.Bytes(want.MemoryBytes), humanize.Duration(time.Since(started))),
		map[string]string{"vcpus": strconv.Itoa(want.VCPUs), "memory_bytes": strconv.FormatInt(want.MemoryBytes, 10)})
	m.logger.InfoContext(ctx, "resized instance",
		"instance", instance.Name, "vcpus", want.VCPUs, "memory_bytes", want.MemoryBytes)
	return nil
}

// resizeVM gives the guest want's memory, then its vCPUs, leaving alone
// what is the same as held. Memory goes first: it is what the guest can
// fail to take, and vCPUs change at once.
func resizeVM(ctx context.Context, hv hypervisor.Hypervisor, held, want types.Resources) error {
	if want.MemoryBytes != held.MemoryBytes {
		ctx, cancel := context.WithTimeout(ctx, resizeMemoryTimeout)
		defer cancel()
		if err := hv.ResizeVMMemory(ctx, want.MemoryBytes); err != nil {
			return err
		}
	}
	if want.VCPUs != held.VCPUs {
		return hv.ResizeVMCPU(ctx, want.VCPUs)
	}
	return nil
}

// checkResize returns ErrInvalidArgument unless an instance holding held can
// be resized to want: a change to its vCPUs or memory needs a maximum for
// them, and stays within it.
func checkResize(instance types.InstanceSpec, held, want types.Resources) error {
	switch {
	case want.VCPUs != held.VCPUs && instance.MaxVCPUs == 0:
		return errdefs.InvalidArgument("instance %q has no max_vcpus, so its vCPUs cannot change while it runs: "+
			"set one while it is stopped", instance.Name)
	case want.VCPUs > instance.MaxVCPUs && instance.MaxVCPUs > 0:
		return errdefs.InvalidArgument("instance %q can have at most %s while it runs, its max_vcpus, not %d",
			instance.Name, humanize.Count(instance.MaxVCPUs, "vCPU"), want.VCPUs)
	case want.MemoryBytes != held.MemoryBytes && instance.MaxMemoryBytes == 0:
		return errdefs.InvalidArgument("instance %q has no max_memory_bytes, so its memory cannot change while it runs: "+
			"set one while it is stopped", instance.Name)
	case want.MemoryBytes > instance.MaxMemoryBytes && instance.MaxMemoryBytes > 0:
		return errdefs.InvalidArgument("instance %q can have at most %s memory while it runs, its max_memory_bytes, not %s",
			instance.Name, humanize.Bytes(instance.MaxMemoryBytes), humanize.Bytes(want.MemoryBytes))
	}
	return nil
}
