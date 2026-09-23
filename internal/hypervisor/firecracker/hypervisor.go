// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/dicer-sh/dicer/internal/hypervisor"
)

// Snapshot file names inside the directory SnapshotVM writes to.
const (
	snapshotStateFile  = "vmstate"
	snapshotMemoryFile = "memory"
)

// Hypervisor controls one Firecracker VMM over its API socket.
type Hypervisor struct {
	client *client
}

var _ hypervisor.Hypervisor = (*Hypervisor)(nil)

// NewHypervisor returns a Hypervisor for the VMM serving its API on
// socketPath.
func NewHypervisor(socketPath string) *Hypervisor {
	return &Hypervisor{client: newClient(socketPath)}
}

// Capabilities reports what Firecracker supports.
func (h *Hypervisor) Capabilities() hypervisor.Capabilities {
	return hypervisor.Capabilities{
		SupportsSnapshot:      true,
		SupportsHotplugMemory: true,
		SupportsPause:         true,
		SupportsVsock:         true,
		SupportsDiskIOLimit:   true,
	}
}

// DestroyVM is unsupported: Firecracker has no way to discard a guest short
// of ending the VMM process.
func (h *Hypervisor) DestroyVM(context.Context) error {
	return fmt.Errorf("firecracker: destroy vm: %w", errors.ErrUnsupported)
}

// ShutdownVM asks the guest to shut down by sending it Ctrl+Alt+Del, which
// Linux treats as a request to reboot. Firecracker exits when the guest
// resets, so this also ends the VMM. It is available on x86 only.
func (h *Hypervisor) ShutdownVM(ctx context.Context) error {
	if runtime.GOARCH != "amd64" {
		return fmt.Errorf("firecracker: Ctrl+Alt+Del on %s: %w", runtime.GOARCH, errors.ErrUnsupported)
	}
	return h.client.put(ctx, "/actions", instanceAction{ActionType: actionSendCtrlAltDel})
}

// Shutdown stops the VMM. Firecracker has no request that ends the process
// directly; it exits when the guest resets, which ShutdownVM brings about.
// Where that is unsupported, the caller has to end the process itself.
func (h *Hypervisor) Shutdown(ctx context.Context) error {
	return h.ShutdownVM(ctx)
}

// GetVMInfo reports the guest's state, and its memory including any
// hotplugged.
func (h *Hypervisor) GetVMInfo(ctx context.Context) (*hypervisor.VirtualMachineInfo, error) {
	var info instanceInfo
	if err := h.client.get(ctx, "/", &info); err != nil {
		return nil, err
	}

	var state hypervisor.VirtualMachineState
	switch info.State {
	case instanceNotStarted:
		state = hypervisor.VirtualMachineStateStopped
	case instanceRunning:
		state = hypervisor.VirtualMachineStateRunning
	case instancePaused:
		state = hypervisor.VirtualMachineStatePaused
	default:
		return nil, fmt.Errorf("firecracker: unknown instance state %q", info.State)
	}

	memory, err := h.memoryBytes(ctx)
	if err != nil {
		return nil, err
	}

	return &hypervisor.VirtualMachineInfo{State: state, MemoryBytes: &memory}, nil
}

// memoryBytes returns the guest's boot memory plus whatever is hotplugged.
func (h *Hypervisor) memoryBytes(ctx context.Context) (int64, error) {
	var cfg vmConfig
	if err := h.client.get(ctx, "/vm/config", &cfg); err != nil {
		return 0, err
	}

	total := int64(cfg.MachineConfig.MemSizeMiB) * mib
	if cfg.MemoryHotplug == nil {
		return total, nil
	}

	var status memoryHotplugStatus
	if err := h.client.get(ctx, "/hotplug/memory", &status); err != nil {
		return 0, err
	}
	return total + int64(status.PluggedSizeMiB)*mib, nil
}

// PauseVM halts the guest's vCPUs.
func (h *Hypervisor) PauseVM(ctx context.Context) error {
	return h.client.patch(ctx, "/vm", vmState{State: vmPaused})
}

// ResumeVM continues a paused guest.
func (h *Hypervisor) ResumeVM(ctx context.Context) error {
	return h.client.patch(ctx, "/vm", vmState{State: vmResumed})
}

// SnapshotVM writes a full snapshot of a paused guest into the directory
// destPath: its device state and its memory, as the files RestoreVM reads.
func (h *Hypervisor) SnapshotVM(ctx context.Context, destPath string) error {
	if err := os.MkdirAll(destPath, 0o700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}

	return h.client.put(ctx, "/snapshot/create", snapshotCreate{
		SnapshotType: "Full",
		SnapshotPath: filepath.Join(destPath, snapshotStateFile),
		MemFilePath:  filepath.Join(destPath, snapshotMemoryFile),
	})
}

// ResizeVMCPU is unsupported: Firecracker cannot hotplug vCPUs.
func (h *Hypervisor) ResizeVMCPU(context.Context, int) error {
	return fmt.Errorf("firecracker: resize vCPUs: %w", errors.ErrUnsupported)
}

// ResizeVMMemory sets the guest's total memory to bytes by plugging or
// unplugging virtio-mem memory. The guest must have booted with a
// hotpluggable region large enough to cover the difference from its boot
// memory.
func (h *Hypervisor) ResizeVMMemory(ctx context.Context, bytes int64) error {
	_, err := h.requestMemory(ctx, bytes)
	return err
}

// ResizeVMMemoryAndWait is ResizeVMMemory, then waits until the guest has
// plugged or unplugged the memory.
func (h *Hypervisor) ResizeVMMemoryAndWait(ctx context.Context, bytes int64, timeout time.Duration) error {
	requested, err := h.requestMemory(ctx, bytes)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		var status memoryHotplugStatus
		if err := h.client.get(ctx, "/hotplug/memory", &status); err != nil {
			return fmt.Errorf("poll memory hotplug: %w", err)
		}
		if status.PluggedSizeMiB == requested {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("memory resize timed out after %s: %d MiB of %d MiB plugged",
				timeout, status.PluggedSizeMiB, requested)
		case <-ticker.C:
		}
	}
}

// requestMemory asks for the hotpluggable region to hold whatever brings the
// guest to bytes in total, returning the size requested in MiB.
func (h *Hypervisor) requestMemory(ctx context.Context, bytes int64) (int, error) {
	var cfg vmConfig
	if err := h.client.get(ctx, "/vm/config", &cfg); err != nil {
		return 0, err
	}
	if cfg.MemoryHotplug == nil {
		return 0, fmt.Errorf("firecracker: resize memory of a guest booted without hotpluggable memory: %w",
			errors.ErrUnsupported)
	}

	base := int64(cfg.MachineConfig.MemSizeMiB) * mib
	requested := ceilDiv(bytes-base, mib)
	if requested < 0 || requested > cfg.MemoryHotplug.TotalSizeMiB {
		return 0, fmt.Errorf("firecracker: memory can be resized between %d MiB and %d MiB, not to %d MiB",
			cfg.MachineConfig.MemSizeMiB, cfg.MachineConfig.MemSizeMiB+cfg.MemoryHotplug.TotalSizeMiB,
			ceilDiv(bytes, mib))
	}

	if err := h.client.patch(ctx, "/hotplug/memory", memoryHotplugUpdate{RequestedSizeMiB: requested}); err != nil {
		return 0, err
	}
	return requested, nil
}
