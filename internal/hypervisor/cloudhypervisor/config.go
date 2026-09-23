// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"github.com/konradasb/dicer/internal/hypervisor"
)

// ToVMConfig translates a Dicer VM specification into Cloud Hypervisor's
// API representation. The serial port is the console; the virtio console is
// off.
func ToVMConfig(spec hypervisor.VirtualMachine) VmConfig {
	return VmConfig{
		Payload: PayloadConfig{
			Kernel:    ptr(spec.Boot.KernelPath),
			Cmdline:   ptr(spec.Boot.KernelArgs),
			Initramfs: ptr(spec.Boot.InitrdPath),
		},
		Cpus:    ptr(cpusConfig(spec.CPU)),
		Memory:  ptr(memoryConfig(spec.Memory)),
		Disks:   ptr(mapSlice(spec.Disks, diskConfig)),
		Serial:  &ConsoleConfig{Mode: ConsoleConfigMode("File"), File: ptr(spec.Console.Path)},
		Console: &ConsoleConfig{Mode: ConsoleConfigMode("Off")},
		Net:     optionalSlice(mapSlice(spec.NICs, netConfig)),
		Vsock:   vsockConfig(spec.Vsock),
		Devices: optionalSlice(mapSlice(spec.Devices, func(d hypervisor.PCIDeviceConfig) DeviceConfig {
			return DeviceConfig{Path: d.Path}
		})),
	}
}

func cpusConfig(c hypervisor.CPUConfig) CpusConfig {
	cpus := CpusConfig{BootVcpus: c.Count, MaxVcpus: c.Count}
	if c.MaxCount > 0 {
		cpus.MaxVcpus = c.MaxCount
	}
	if len(c.Affinity) > 0 {
		cpus.Affinity = ptr(mapSlice(c.Affinity, func(a hypervisor.CPUAffinity) CpuAffinity {
			return CpuAffinity{Vcpu: a.VCPU, HostCpus: a.HostCPUs}
		}))
	}
	if t := c.Topology; t != nil {
		cpus.Topology = &CpuTopology{
			ThreadsPerCore: ptr(t.ThreadsPerCore),
			CoresPerDie:    ptr(t.CoresPerDie),
			DiesPerPackage: ptr(t.DiesPerPackage),
			Packages:       ptr(t.Packages),
		}
	}
	return cpus
}

func memoryConfig(m hypervisor.MemoryConfig) MemoryConfig {
	memory := MemoryConfig{Size: m.SizeBytes}
	if m.HotplugBytes > 0 {
		memory.HotplugSize = ptr(m.HotplugBytes)
		memory.HotplugMethod = ptr("VirtioMem")
	}
	return memory
}

// diskConfig translates a disk. A rate limit is a token bucket refilled
// every second, so its size is the rate in bytes per second, with the burst
// above that rate as a one-time allowance.
func diskConfig(d hypervisor.DiskConfig) DiskConfig {
	disk := DiskConfig{Path: ptr(d.Path)}
	if d.ReadOnly {
		disk.Readonly = ptr(true)
	}
	if d.RateLimitBps > 0 {
		burst := max(d.RateLimitBurstBps, d.RateLimitBps)
		disk.RateLimiterConfig = &RateLimiterConfig{
			Bandwidth: &TokenBucket{
				Size:         d.RateLimitBps,
				RefillTime:   1000,
				OneTimeBurst: ptr(burst - d.RateLimitBps),
			},
		}
	}
	return disk
}

func netConfig(n hypervisor.NetworkInterfaceConfig) NetConfig {
	nc := NetConfig{Tap: ptr(n.TapDevice), Ip: ptr(n.IP), Mac: ptr(n.MAC), Mask: ptr(n.Netmask)}
	if n.MTU > 0 {
		nc.Mtu = ptr(n.MTU)
	}
	return nc
}

func vsockConfig(v *hypervisor.VsockConfig) *VsockConfig {
	if v == nil {
		return nil
	}
	return &VsockConfig{Cid: int64(v.CID), Socket: v.Socket}
}

// mapSlice returns f applied to each element of in.
func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// optionalSlice returns a pointer to s, or nil if s is empty, for fields the
// API omits when there is nothing in them.
func optionalSlice[T any](s []T) *[]T {
	if len(s) == 0 {
		return nil
	}
	return &s
}

func ptr[T any](v T) *T {
	return &v
}
