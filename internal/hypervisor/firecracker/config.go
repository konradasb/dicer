// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"context"
	"errors"
	"fmt"

	"github.com/dicer-sh/dicer/internal/hypervisor"
)

const (
	mib = 1 << 20

	// maxVCPUs is the most vCPUs Firecracker gives a guest.
	maxVCPUs = 32

	// hotplugSlotMiB is the default virtio-mem slot size. The hotpluggable
	// region is rounded up to a whole number of slots.
	hotplugSlotMiB = 128
)

// setup is everything Firecracker is told about a guest before it boots.
// Each field maps to one pre-boot API resource.
type setup struct {
	boot    bootSource
	machine machineConfig
	drives  []drive
	nics    []networkInterface
	vsock   *vsock
	hotplug *memoryHotplugConfig
	serial  *serialDevice
}

// newSetup translates a Dicer VM specification into Firecracker's terms,
// rejecting what Firecracker cannot do rather than silently dropping it.
func newSetup(spec hypervisor.VirtualMachine) (*setup, error) {
	if err := checkSupported(spec); err != nil {
		return nil, err
	}

	s := &setup{
		boot: bootSource{
			KernelImagePath: spec.Boot.KernelPath,
			BootArgs:        spec.Boot.KernelArgs,
			InitrdPath:      spec.Boot.InitrdPath,
		},
		machine: machineConfig{
			VCPUCount:  spec.CPU.Count,
			MemSizeMiB: ceilDiv(spec.Memory.SizeBytes, mib),
		},
	}

	// Firecracker attaches drives in the order they are configured, so the
	// guest sees them as vda, vdb, ... in spec order, as it does under
	// Cloud Hypervisor. None is the root device: the guest boots from the
	// initramfs, which mounts its root itself.
	for i, d := range spec.Disks {
		s.drives = append(s.drives, drive{
			DriveID:     fmt.Sprintf("disk%d", i),
			PathOnHost:  d.Path,
			IsReadOnly:  d.ReadOnly,
			RateLimiter: diskRateLimiter(d),
		})
	}

	for i, n := range spec.NICs {
		s.nics = append(s.nics, networkInterface{
			IfaceID:     fmt.Sprintf("eth%d", i),
			HostDevName: n.TapDevice,
			GuestMAC:    n.MAC,
			MTU:         n.MTU,
		})
	}

	if spec.Vsock != nil {
		s.vsock = &vsock{GuestCID: spec.Vsock.CID, UDSPath: spec.Vsock.Socket}
	}

	if spec.Memory.HotplugBytes > 0 {
		slots := ceilDiv(spec.Memory.HotplugBytes, hotplugSlotMiB*mib)
		s.hotplug = &memoryHotplugConfig{TotalSizeMiB: slots * hotplugSlotMiB}
	}

	if spec.Console.Path != "" {
		s.serial = &serialDevice{SerialOutPath: spec.Console.Path}
	}

	return s, nil
}

// checkSupported rejects the parts of a specification Firecracker has no
// equivalent for.
func checkSupported(spec hypervisor.VirtualMachine) error {
	switch {
	case spec.CPU.Count < 1 || spec.CPU.Count > maxVCPUs:
		return fmt.Errorf("firecracker supports 1 to %d vCPUs, not %d", maxVCPUs, spec.CPU.Count)
	case spec.CPU.MaxCount > spec.CPU.Count:
		return fmt.Errorf("firecracker: vCPU hotplug: %w", errors.ErrUnsupported)
	case spec.CPU.Topology != nil:
		return fmt.Errorf("firecracker: CPU topology: %w", errors.ErrUnsupported)
	case len(spec.CPU.Affinity) > 0:
		return fmt.Errorf("firecracker: CPU affinity: %w", errors.ErrUnsupported)
	case len(spec.Devices) > 0:
		return fmt.Errorf("firecracker: PCI passthrough: %w", errors.ErrUnsupported)
	case spec.GPU != nil:
		return fmt.Errorf("firecracker: GPU: %w", errors.ErrUnsupported)
	case spec.Memory.SizeBytes <= 0:
		return errors.New("firecracker: memory size must be positive")
	}
	return nil
}

// diskRateLimiter converts a disk's byte rate limit into Firecracker's token
// bucket: RateLimitBps tokens refilled every second, plus the burst above
// the rate as a one-off allowance.
func diskRateLimiter(d hypervisor.DiskConfig) *rateLimiter {
	if d.RateLimitBps <= 0 {
		return nil
	}

	bucket := &tokenBucket{Size: d.RateLimitBps, RefillTime: 1000}
	if d.RateLimitBurstBps > d.RateLimitBps {
		bucket.OneTimeBurst = d.RateLimitBurstBps - d.RateLimitBps
	}
	return &rateLimiter{Bandwidth: bucket}
}

// apply sends the setup to a Firecracker process that has not yet booted.
func (s *setup) apply(ctx context.Context, c *client) error {
	if err := c.put(ctx, "/boot-source", s.boot); err != nil {
		return err
	}
	if err := c.put(ctx, "/machine-config", s.machine); err != nil {
		return err
	}
	for _, d := range s.drives {
		if err := c.put(ctx, "/drives/"+d.DriveID, d); err != nil {
			return err
		}
	}
	for _, n := range s.nics {
		if err := c.put(ctx, "/network-interfaces/"+n.IfaceID, n); err != nil {
			return err
		}
	}
	if s.vsock != nil {
		if err := c.put(ctx, "/vsock", s.vsock); err != nil {
			return err
		}
	}
	if s.hotplug != nil {
		if err := c.put(ctx, "/hotplug/memory", s.hotplug); err != nil {
			return err
		}
	}
	if s.serial != nil {
		if err := c.put(ctx, "/serial", s.serial); err != nil {
			return err
		}
	}
	return nil
}

// ceilDiv returns n/d rounded up, as an int.
func ceilDiv(n, d int64) int {
	return int((n + d - 1) / d)
}
