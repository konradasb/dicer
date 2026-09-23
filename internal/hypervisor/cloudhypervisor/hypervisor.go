// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/dicer-sh/dicer/internal/hypervisor"
)

// Hypervisor controls one Cloud Hypervisor VMM over its API socket.
type Hypervisor struct {
	client ClientWithResponsesInterface
}

// NewHypervisor returns a Hypervisor for the VMM serving its API on
// socketPath.
func NewHypervisor(socketPath string) (*Hypervisor, error) {
	client, err := NewClientWithResponsesFromSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("create vmm client: %w", err)
	}

	return &Hypervisor{client: client}, nil
}

var _ hypervisor.Hypervisor = (*Hypervisor)(nil)

// Capabilities returns the features supported by Cloud Hypervisor.
func (h *Hypervisor) Capabilities() hypervisor.Capabilities {
	capabilities := hypervisor.Capabilities{
		SupportsSnapshot:       true,
		SupportsHotplugMemory:  true,
		SupportsHotplugCPU:     true,
		SupportsCPUAffinity:    true,
		SupportsPause:          true,
		SupportsVsock:          true,
		SupportsGPUPassthrough: true,
		SupportsDiskIOLimit:    true,
	}

	return capabilities
}

// DestroyVM implements hypervisor.Hypervisor.
func (h *Hypervisor) DestroyVM(ctx context.Context) error {
	resp, err := h.client.DeleteVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("destroy vm: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("destroy vm: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// ShutdownVM implements hypervisor.Hypervisor by pressing the guest's ACPI
// power button.
func (h *Hypervisor) ShutdownVM(ctx context.Context) error {
	resp, err := h.client.PowerButtonVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("press power button: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("press power button: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// Shutdown implements hypervisor.Hypervisor.
func (h *Hypervisor) Shutdown(ctx context.Context) error {
	resp, err := h.client.ShutdownVMMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("shutdown vmm: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("shutdown vmm: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// GetVMInfo implements hypervisor.Hypervisor.
func (h *Hypervisor) GetVMInfo(ctx context.Context) (*hypervisor.VirtualMachineInfo, error) {
	resp, err := h.client.GetVmInfoWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("get vm info: %w", err)
	}
	// Cloud Hypervisor returns 500 when no VM is configured (e.g. after
	// DestroyVM). No active guest means the instance is Stopped.
	if resp.StatusCode() == 500 {
		return &hypervisor.VirtualMachineInfo{State: hypervisor.VirtualMachineStateStopped}, nil
	}

	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("get vm info: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	var state hypervisor.VirtualMachineState
	switch resp.JSON200.State {
	case Created, Shutdown:
		// "Created" (VMM started, no active VM) and "Shutdown" (guest shut down,
		// VMM still alive) both mean no guest is actively running.
		state = hypervisor.VirtualMachineStateStopped
	case Running:
		state = hypervisor.VirtualMachineStateRunning
	case Paused:
		state = hypervisor.VirtualMachineStatePaused
	default:
		return nil, fmt.Errorf("unknown vm state %q", resp.JSON200.State)
	}

	vminfo := &hypervisor.VirtualMachineInfo{
		State:       state,
		MemoryBytes: resp.JSON200.MemoryActualSize,
	}

	return vminfo, nil
}

// PauseVM implements hypervisor.Hypervisor.
func (h *Hypervisor) PauseVM(ctx context.Context) error {
	resp, err := h.client.PauseVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("pause vm: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("pause vm: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// ResumeVM implements hypervisor.Hypervisor.
func (h *Hypervisor) ResumeVM(ctx context.Context) error {
	resp, err := h.client.ResumeVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("resume vm: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("resume vm: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// SnapshotVM implements hypervisor.Hypervisor.
func (h *Hypervisor) SnapshotVM(ctx context.Context, destPath string) error {
	snapshotURL := "file://" + destPath
	snapshotConfig := VmSnapshotConfig{DestinationUrl: &snapshotURL}
	resp, err := h.client.PutVmSnapshotWithResponse(ctx, snapshotConfig)
	if err != nil {
		return fmt.Errorf("snapshot vm: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("snapshot vm: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// ResizeVMCPU implements hypervisor.Hypervisor.
func (h *Hypervisor) ResizeVMCPU(ctx context.Context, count int) error {
	resizeConfig := VmResize{DesiredVcpus: &count}
	resp, err := h.client.PutVmResizeWithResponse(ctx, resizeConfig)
	if err != nil {
		return fmt.Errorf("resize cpu: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("resize cpu: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// ResizeVMMemory implements hypervisor.Hypervisor.
func (h *Hypervisor) ResizeVMMemory(ctx context.Context, bytes int64) error {
	resizeConfig := VmResize{DesiredRam: &bytes}
	resp, err := h.client.PutVmResizeWithResponse(ctx, resizeConfig)
	if err != nil {
		return fmt.Errorf("resize memory: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("resize memory: status %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// ResizeVMMemoryAndWait implements hypervisor.Hypervisor.
func (h *Hypervisor) ResizeVMMemoryAndWait(ctx context.Context, bytes int64, timeout time.Duration) error {
	if err := h.ResizeVMMemory(ctx, bytes); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	const (
		pollInterval         = 20 * time.Millisecond
		requiredStableChecks = 3
	)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastSize int64 = -1
	stableCount := 0

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("memory resize timed out after %s", timeout)
		case <-ticker.C:
			info, err := h.GetVMInfo(ctx)
			if err != nil {
				return fmt.Errorf("poll memory size: %w", err)
			}
			if info.MemoryBytes == nil {
				// Cloud Hypervisor does not report the guest's memory size; assume the resize succeeded.
				return nil
			}
			currentSize := *info.MemoryBytes
			if currentSize == lastSize {
				stableCount++
				if stableCount >= requiredStableChecks {
					return nil
				}
			} else {
				stableCount = 0
				lastSize = currentSize
			}
		}
	}
}
