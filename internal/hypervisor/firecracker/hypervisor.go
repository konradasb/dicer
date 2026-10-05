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

	"github.com/konradasb/dicer/internal/hypervisor"
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
// socketPath. It does not connect until the first request.
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
		SupportsDiskRateLimit: true,
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
	if err := h.client.put(ctx, "/actions", instanceAction{ActionType: actionSendCtrlAltDel}); err != nil {
		return fmt.Errorf("send ctrl+alt+del: %w", err)
	}
	return nil
}

// Shutdown stops the VMM. Firecracker has no request that ends the process
// directly; it exits when the guest resets, which ShutdownVM brings about.
// Where that is unsupported, the caller has to end the process itself.
func (h *Hypervisor) Shutdown(ctx context.Context) error {
	return h.ShutdownVM(ctx)
}

// VMInfo reports the guest's state, and its memory including any
// hotplugged.
func (h *Hypervisor) VMInfo(ctx context.Context) (*hypervisor.VMInfo, error) {
	var info instanceInfo
	if err := h.client.get(ctx, "/", &info); err != nil {
		return nil, fmt.Errorf("get vm info: %w", err)
	}

	var state hypervisor.VMState
	switch info.State {
	case instanceNotStarted:
		state = hypervisor.VMStateStopped
	case instanceRunning:
		state = hypervisor.VMStateRunning
	case instancePaused:
		state = hypervisor.VMStatePaused
	default:
		return nil, fmt.Errorf("firecracker: unknown instance state %q", info.State)
	}

	memory, err := h.memoryBytes(ctx)
	if err != nil {
		return nil, fmt.Errorf("get vm info: %w", err)
	}

	return &hypervisor.VMInfo{State: state, MemoryBytes: &memory}, nil
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
	if err := h.client.patch(ctx, "/vm", vmState{State: vmPaused}); err != nil {
		return fmt.Errorf("pause vm: %w", err)
	}
	return nil
}

// ResumeVM continues a paused guest.
func (h *Hypervisor) ResumeVM(ctx context.Context) error {
	if err := h.client.patch(ctx, "/vm", vmState{State: vmResumed}); err != nil {
		return fmt.Errorf("resume vm: %w", err)
	}
	return nil
}

// SnapshotVM writes a full snapshot of a paused guest into the directory
// destPath: its device state and its memory, as the files RestoreVM reads.
func (h *Hypervisor) SnapshotVM(ctx context.Context, destPath string) error {
	if err := os.MkdirAll(destPath, 0o700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}

	err := h.client.put(ctx, "/snapshot/create", snapshotCreate{
		SnapshotType: "Full",
		SnapshotPath: filepath.Join(destPath, snapshotStateFile),
		MemFilePath:  filepath.Join(destPath, snapshotMemoryFile),
	})
	if err != nil {
		return fmt.Errorf("snapshot vm: %w", err)
	}
	return nil
}

// ResizeVMCPU is unsupported: Firecracker cannot hotplug vCPUs.
func (h *Hypervisor) ResizeVMCPU(context.Context, int) error {
	return fmt.Errorf("firecracker: resize vCPUs: %w", errors.ErrUnsupported)
}

// ResizeVMMemory sets the guest's total memory to bytes by plugging or
// unplugging virtio-mem memory, and waits until the guest has, or ctx is
// done.
func (h *Hypervisor) ResizeVMMemory(ctx context.Context, bytes int64) error {
	var cfg vmConfig
	if err := h.client.get(ctx, "/vm/config", &cfg); err != nil {
		return fmt.Errorf("resize memory: %w", err)
	}
	bootBytes := int64(cfg.MachineConfig.MemSizeMiB) * mib
	hotplugBytes := int64(0)
	if cfg.MemoryHotplug != nil {
		hotplugBytes = int64(cfg.MemoryHotplug.TotalSizeMiB) * mib
	}
	if err := hypervisor.CheckMemoryResize(bytes, bootBytes, hotplugBytes); err != nil {
		return fmt.Errorf("firecracker: %w", err)
	}

	requested := int((bytes - bootBytes) / mib)
	if err := h.client.patch(ctx, "/hotplug/memory", memoryHotplugUpdate{RequestedSizeMiB: requested}); err != nil {
		return fmt.Errorf("resize memory: %w", err)
	}

	const pollInterval = 20 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		var status memoryHotplugStatus
		if err := h.client.get(ctx, "/hotplug/memory", &status); err != nil {
			return fmt.Errorf("wait for the guest to resize its memory: %w", err)
		}
		if status.PluggedSizeMiB == requested {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for the guest to resize its memory: %d of %d MiB plugged: %w",
				status.PluggedSizeMiB, requested, ctx.Err())
		case <-ticker.C:
		}
	}
}
