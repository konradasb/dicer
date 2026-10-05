// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"context"
	"errors"
	"fmt"

	"github.com/konradasb/dicer/internal/hypervisor"
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
	boot              bootSource
	machine           machineConfig
	drives            []drive
	networkInterfaces []networkInterface
	vsock             *vsock
	hotplug           *memoryHotplugConfig
	serial            *serialDevice
}

// newSetup translates a Dicer VM specification into Firecracker's terms,
// rejecting what Firecracker cannot do rather than silently dropping it.
func newSetup(spec hypervisor.VMSpec) (*setup, error) {
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
			MemSizeMiB: divideRoundingUp(spec.Memory.SizeBytes, mib),
		},
	}

	// Drives appear as vda, vdb, ... in spec order. None is the root
	// device; the initramfs mounts the root.
	for i, d := range spec.Disks {
		s.drives = append(s.drives, drive{
			DriveID:     fmt.Sprintf("disk%d", i),
			PathOnHost:  d.Path,
			IsReadOnly:  d.ReadOnly,
			RateLimiter: diskRateLimiter(d),
		})
	}

	for i, n := range spec.NetworkInterfaces {
		s.networkInterfaces = append(s.networkInterfaces, networkInterface{
			IfaceID:     fmt.Sprintf("eth%d", i),
			HostDevName: n.TAPDevice,
			GuestMAC:    n.MAC,
			MTU:         n.MTU,
		})
	}

	if spec.Vsock != nil {
		s.vsock = &vsock{GuestCID: spec.Vsock.CID, UDSPath: spec.Vsock.SocketPath}
	}

	if spec.Memory.HotplugBytes > 0 {
		slots := divideRoundingUp(spec.Memory.HotplugBytes, hotplugSlotMiB*mib)
		s.hotplug = &memoryHotplugConfig{TotalSizeMiB: slots * hotplugSlotMiB}
	}

	if spec.Console.Path != "" {
		s.serial = &serialDevice{SerialOutPath: spec.Console.Path}
	}

	return s, nil
}

// checkSupported rejects the parts of a specification Firecracker has no
// equivalent for.
func checkSupported(spec hypervisor.VMSpec) error {
	switch {
	case spec.CPU.Count < 1 || spec.CPU.Count > maxVCPUs:
		return fmt.Errorf("firecracker supports 1 to %d vCPUs, not %d", maxVCPUs, spec.CPU.Count)
	case spec.CPU.MaxCount > spec.CPU.Count:
		return fmt.Errorf("firecracker: vCPU hotplug: %w", errors.ErrUnsupported)
	case spec.CPU.Topology != nil:
		return fmt.Errorf("firecracker: CPU topology: %w", errors.ErrUnsupported)
	case len(spec.CPU.Affinity) > 0:
		return fmt.Errorf("firecracker: CPU affinity: %w", errors.ErrUnsupported)
	case len(spec.PCIDevices) > 0:
		return fmt.Errorf("firecracker: PCI passthrough: %w", errors.ErrUnsupported)
	case spec.GPU != nil:
		return fmt.Errorf("firecracker: GPU: %w", errors.ErrUnsupported)
	case spec.Memory.SizeBytes <= 0:
		return errors.New("firecracker: memory size must be positive")
	}
	return nil
}

// diskRateLimiter converts a disk's rate limit into Firecracker's token
// buckets, each holding what the disk may do in a second, refilled every
// second. An unlimited disk has none.
func diskRateLimiter(d hypervisor.DiskConfig) *rateLimiter {
	if d.RateLimitBytesPerSecond <= 0 && d.RateLimitIOPS <= 0 {
		return nil
	}
	return &rateLimiter{
		Bandwidth: perSecondBucket(d.RateLimitBytesPerSecond),
		Ops:       perSecondBucket(d.RateLimitIOPS),
	}
}

// perSecondBucket returns a token bucket refilled with n tokens every
// second, or nil for none if n is zero.
func perSecondBucket(n int64) *tokenBucket {
	if n <= 0 {
		return nil
	}
	return &tokenBucket{Size: n, RefillTime: 1000}
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
	for _, n := range s.networkInterfaces {
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

// divideRoundingUp returns n/d rounded up, as an int.
func divideRoundingUp(n, d int64) int {
	return int((n + d - 1) / d)
}
