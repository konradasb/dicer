// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package hypervisor defines the VM specification and the operations a
// virtual machine monitor must support. Implementations live in subpackages.
package hypervisor

import (
	"context"
	"time"

	"github.com/konradasb/dicer/internal/process"
)

// Hypervisor controls a single running virtual machine.
type Hypervisor interface {
	// DestroyVM immediately and forcefully destroys the guest without sending
	// any ACPI signal. Use ShutdownVM for a graceful guest shutdown instead.
	DestroyVM(ctx context.Context) error

	// ShutdownVM sends an ACPI power-off signal to the guest OS, allowing it
	// to shut down gracefully. The VMM process remains running afterwards.
	ShutdownVM(ctx context.Context) error

	// Shutdown stops the VMM process itself.
	Shutdown(ctx context.Context) error

	// VMInfo returns the VM's state and memory.
	VMInfo(ctx context.Context) (*VMInfo, error)

	// PauseVM halts the vCPUs; ResumeVM continues them.
	PauseVM(ctx context.Context) error
	ResumeVM(ctx context.Context) error

	// SnapshotVM writes the VM's state to destPath. The VM must be paused.
	SnapshotVM(ctx context.Context, destPath string) error

	// ResizeVMMemory and ResizeVMCPU hotplug memory and vCPUs.
	// ResizeVMMemoryAndWait also waits for the guest to accept the memory.
	ResizeVMMemory(ctx context.Context, bytes int64) error
	ResizeVMMemoryAndWait(ctx context.Context, bytes int64, timeout time.Duration) error
	ResizeVMCPU(ctx context.Context, count int) error

	// Capabilities reports which of the optional operations are supported.
	Capabilities() Capabilities
}

// Capabilities reports which optional features a hypervisor supports.
// Callers check them before using a feature.
type Capabilities struct {
	// SupportsSnapshot reports whether SnapshotVM and Starter.RestoreVM work.
	SupportsSnapshot bool

	// SupportsHotplugMemory reports whether ResizeVMMemory and
	// ResizeVMMemoryAndWait work.
	SupportsHotplugMemory bool

	// SupportsHotplugCPU reports whether ResizeVMCPU works.
	SupportsHotplugCPU bool

	// SupportsCPUAffinity reports whether vCPUs can be pinned to host CPUs.
	SupportsCPUAffinity bool

	// SupportsPause reports whether PauseVM and ResumeVM work.
	SupportsPause bool

	// SupportsVsock reports whether the guest can have a vsock device.
	SupportsVsock bool

	// SupportsGPUPassthrough reports whether host PCI devices, GPUs among
	// them, can be passed through to the guest.
	SupportsGPUPassthrough bool

	// SupportsDiskRateLimit reports whether a disk's reads and writes can
	// be rate limited.
	SupportsDiskRateLimit bool
}

// Starter launches and connects to VMMs of one hypervisor version. The VMM
// process must have socketPath among its arguments, so process.Attach can
// identify it after a daemon restart.
type Starter interface {
	// Version returns the hypervisor binary version string (e.g. "v49.0").
	Version() string

	// DefaultKernelArgs returns the kernel arguments this hypervisor needs
	// the guest booted with, such as its console device and what a panic
	// does. They are used when the instance sets no kernel arguments of its
	// own.
	DefaultKernelArgs() string

	// PowerOffEndsVM reports whether a guest powering off ends the VMM. If
	// not, the guest resets instead, which DefaultKernelArgs makes end it.
	PowerOffEndsVM() bool

	// StartVM launches a VMM and boots the virtual machine with the given
	// configuration. The returned process is the VMM; the caller owns it from
	// then on.
	StartVM(ctx context.Context, socketPath string, spec VMSpec) (vmm *process.Process, hypervisor Hypervisor, err error)

	// RestoreVM launches a VMM and restores the VM from a snapshot
	// at the given path, leaving it paused. The console is passed because
	// not every snapshot carries it.
	RestoreVM(
		ctx context.Context, socketPath string, snapshotPath string, console ConsoleConfig,
	) (vmm *process.Process, hypervisor Hypervisor, err error)

	// Connect returns a Hypervisor for a VMM already serving its API on
	// socketPath, such as one started before the daemon restarted.
	Connect(socketPath string) (Hypervisor, error)
}
