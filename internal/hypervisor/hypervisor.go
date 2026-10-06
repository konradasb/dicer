// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package hypervisor defines the VM specification and the operations a
// virtual machine monitor must support. Implementations live in subpackages.
package hypervisor

import (
	"context"

	"github.com/konradasb/dicer/internal/process"
)

// Hypervisor controls a single running virtual machine. Each request is
// bounded by ctx's deadline or, if it has none, by a timeout of the
// implementation's, long enough for anything but SnapshotVM.
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

	// SnapshotVM writes the VM's state, its memory included, into the
	// directory destPath. The VM must be paused. Writing a large guest's
	// memory can take minutes: ctx should have a deadline to suit.
	SnapshotVM(ctx context.Context, destPath string) error

	// ResizeVMMemory sets the guest's memory, from the memory it booted with
	// up to that and MemoryConfig.HotplugBytes, in steps of
	// MemoryResizeStep; another size is refused with
	// errdefs.ErrInvalidArgument, before anything changes. Where the
	// hypervisor can tell, it returns once the guest has taken or given up
	// the memory, or ctx is done.
	ResizeVMMemory(ctx context.Context, bytes int64) error

	// ResizeVMCPU sets the guest's vCPUs, up to CPUConfig.MaxCount.
	ResizeVMCPU(ctx context.Context, count int) error

	// Capabilities reports which of the optional operations are supported.
	Capabilities() Capabilities
}

// Capabilities reports which optional features a hypervisor supports.
// Callers check them before using a feature.
type Capabilities struct {
	// SupportsSnapshot reports whether SnapshotVM and Starter.RestoreVM work.
	SupportsSnapshot bool

	// SupportsHotplugMemory reports whether ResizeVMMemory works.
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
// identify it after a daemon restart, and runs in the directory holding
// socketPath: a relative path in a VMSpec resolves there, both when the VM
// boots and when a snapshot of it is restored, maybe by another VMM in
// another directory. The caller clears stale sockets from the directory.
type Starter interface {
	// Version returns the hypervisor binary version string (e.g. "v53.0.0").
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

	// RestoreVM launches a VMM and restores the VM from the snapshot
	// SnapshotVM wrote into the directory snapshotPath, leaving it paused,
	// with what spec gives it in place of what the snapshot holds. As with
	// SnapshotVM, ctx should have a deadline that suits the guest's memory.
	RestoreVM(
		ctx context.Context, socketPath string, snapshotPath string, spec RestoreSpec,
	) (vmm *process.Process, hypervisor Hypervisor, err error)

	// Connect returns a Hypervisor for a VMM already serving its API on
	// socketPath, such as one started before the daemon restarted.
	Connect(socketPath string) (Hypervisor, error)
}
