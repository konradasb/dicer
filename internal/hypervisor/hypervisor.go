// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package hypervisor abstracts the virtual machine monitor.
//
// It defines the VM specification Dicer builds and the operations a VMM must
// support to run it, so that the lifecycle code in internal/vm is written
// against a capability set rather than against Cloud Hypervisor's API.
// Implementations live in subpackages.
package hypervisor

import (
	"context"
	"time"

	"github.com/dicer-sh/dicer/internal/process"
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

	// GetVMInfo reports the VM's state and configuration.
	GetVMInfo(ctx context.Context) (*VirtualMachineInfo, error)

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

// Capabilities indicates which optional features a hypervisor supports.
// Callers should check these before calling optional methods.
type Capabilities struct {
	// SupportsSnapshot indicates if Snapshot/Restore are available
	SupportsSnapshot bool

	// SupportsHotplugMemory indicates if ResizeMemory/ResizeMemoryAndWait are available.
	SupportsHotplugMemory bool

	// SupportsHotplugCPU indicates if ResizeCPU is available.
	SupportsHotplugCPU bool

	// SupportsCPUAffinity indicates if per-vCPU host-CPU pinning is available.
	SupportsCPUAffinity bool

	// SupportsPause indicates if Pause/Resume are available
	SupportsPause bool

	// SupportsVsock indicates if vsock communication is available
	SupportsVsock bool

	// SupportsGPUPassthrough indicates if PCI device passthrough is available
	SupportsGPUPassthrough bool

	// SupportsDiskIOLimit indicates if disk I/O rate limiting is available
	SupportsDiskIOLimit bool
}

// Starter handles the full VM startup sequence.
// Each implementation embeds its own binary path; callers only provide
// runtime parameters (socket path, VM config).
//
// The VMM process an implementation launches must carry socketPath as one of
// its command-line arguments. That is how a restarted daemon tells the VMM it
// recorded from an unrelated process that has since been given the same PID;
// see process.Attach.
type Starter interface {
	// Version returns the hypervisor binary version string (e.g. "v49.0").
	Version() string

	// DefaultBootArgs returns the kernel command-line arguments this hypervisor
	// requires for correct operation (e.g. console device, panic behaviour).
	// Used when the instance sets no kernel arguments of its own.
	DefaultBootArgs() string

	// PowerOffEndsVM reports whether a guest powering itself off ends the
	// VMM. Where it does not, the guest ends it by resetting instead: with
	// the reset-on-reboot DefaultBootArgs ask for, a VMM that does not power
	// off guests exits on a reset.
	PowerOffEndsVM() bool

	// StartVM launches the hypervisor process and boots the virtual machine
	// with the given configuration. The returned process is the VMM; the
	// caller owns it from then on.
	StartVM(ctx context.Context, socketPath string, spec VirtualMachine) (vmm *process.Process, hv Hypervisor, err error)

	// RestoreVM launches the hypervisor process and restores the virtual
	// machine from a snapshot at the given path. The VM is left paused; call
	// ResumeVM to continue it.
	//
	// The console is passed separately because it is host-side configuration
	// that a snapshot need not carry: Firecracker's does not, and a guest
	// restored without it writes its console nowhere.
	RestoreVM(
		ctx context.Context, socketPath string, snapshotPath string, console ConsoleConfig,
	) (vmm *process.Process, hv Hypervisor, err error)

	// Connect creates a Hypervisor client for an already-running VMM at the given socket.
	// Used to reconnect to a VMM process for control operations (stop, resize, etc.).
	Connect(socketPath string) (Hypervisor, error)
}
