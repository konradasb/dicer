// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"path"

	"github.com/konradasb/dicer/internal/hypervisor"
)

// mediatedDeviceDir is the sysfs directory holding the host's mediated
// devices, by UUID. VFIO opens a mediated device by its path there.
const mediatedDeviceDir = "/sys/bus/mdev/devices"

// vmConfig translates a VM specification into Cloud Hypervisor's API
// representation. The serial port is the console; the virtio console is
// off.
func vmConfig(spec hypervisor.VMSpec) VmConfig {
	return VmConfig{
		Payload: PayloadConfig{
			Kernel:    ptr(spec.Boot.KernelPath),
			Cmdline:   ptr(spec.Boot.KernelArgs),
			Initramfs: ptr(spec.Boot.InitrdPath),
		},
		Cpus:    ptr(cpusConfig(spec.CPU)),
		Memory:  ptr(memoryConfig(spec.Memory)),
		Disks:   ptr(mapSlice(spec.Disks, diskConfig)),
		Serial:  &ConsoleConfig{Mode: ConsoleConfigModeFile, File: ptr(spec.Console.Path)},
		Console: &ConsoleConfig{Mode: ConsoleConfigModeOff},
		Net:     optionalSlice(mapSlice(spec.NetworkInterfaces, netConfig)),
		Vsock:   vsockConfig(spec.Vsock),
		Devices: optionalSlice(deviceConfigs(spec)),
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

// virtioMemAlignment is what Cloud Hypervisor requires the memory set aside
// for virtio-mem to be a multiple of.
const virtioMemAlignment = 128 << 20

// memoryConfig translates the memory, rounding what is set aside for hotplug
// up to virtioMemAlignment.
func memoryConfig(m hypervisor.MemoryConfig) MemoryConfig {
	memory := MemoryConfig{Size: m.SizeBytes}
	if m.HotplugBytes > 0 {
		memory.HotplugSize = ptr((m.HotplugBytes + virtioMemAlignment - 1) / virtioMemAlignment * virtioMemAlignment)
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
	if d.RateLimitBytesPerSecond > 0 {
		burst := max(d.RateLimitBurstBytesPerSecond, d.RateLimitBytesPerSecond)
		disk.RateLimiterConfig = &RateLimiterConfig{
			Bandwidth: &TokenBucket{
				Size:         d.RateLimitBytesPerSecond,
				RefillTime:   1000,
				OneTimeBurst: ptr(burst - d.RateLimitBytesPerSecond),
			},
		}
	}
	return disk
}

func netConfig(n hypervisor.NetworkInterfaceConfig) NetConfig {
	net := NetConfig{Tap: ptr(n.TAPDevice), Ip: ptr(n.IP), Mac: ptr(n.MAC), Mask: ptr(n.Netmask)}
	if n.MTU > 0 {
		net.Mtu = ptr(n.MTU)
	}
	return net
}

func vsockConfig(v *hypervisor.VsockConfig) *VsockConfig {
	if v == nil {
		return nil
	}
	return &VsockConfig{Cid: int64(v.CID), Socket: v.SocketPath}
}

// deviceConfigs lists the host devices passed through to the guest over
// VFIO: the PCI devices, then the GPU's mediated device.
func deviceConfigs(spec hypervisor.VMSpec) []DeviceConfig {
	devices := mapSlice(spec.PCIDevices, func(d hypervisor.PCIDeviceConfig) DeviceConfig {
		return DeviceConfig{Path: d.Path}
	})
	if spec.GPU != nil {
		devices = append(devices, DeviceConfig{
			Path: path.Join(mediatedDeviceDir, spec.GPU.MediatedDeviceUUID),
		})
	}
	return devices
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
