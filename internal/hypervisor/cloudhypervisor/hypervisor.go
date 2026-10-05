// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/konradasb/dicer/internal/hypervisor"
)

// apiTimeout bounds a request to the VMM's API whose context has no
// deadline.
const apiTimeout = 30 * time.Second

// Hypervisor controls one Cloud Hypervisor VMM over its API socket.
type Hypervisor struct {
	client *ClientWithResponses
}

var _ hypervisor.Hypervisor = (*Hypervisor)(nil)

// NewHypervisor returns a Hypervisor for the VMM serving its API on
// socketPath. It does not connect until the first request.
func NewHypervisor(socketPath string) *Hypervisor {
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
			DisableKeepAlives: true,
		},
	}
	client := &Client{Server: "http://localhost/api/v1/", Client: statusCheckingClient{httpClient}}
	return &Hypervisor{client: &ClientWithResponses{ClientInterface: client}}
}

// Capabilities reports what Cloud Hypervisor supports: everything.
func (h *Hypervisor) Capabilities() hypervisor.Capabilities {
	return hypervisor.Capabilities{
		SupportsSnapshot:       true,
		SupportsHotplugMemory:  true,
		SupportsHotplugCPU:     true,
		SupportsCPUAffinity:    true,
		SupportsPause:          true,
		SupportsVsock:          true,
		SupportsGPUPassthrough: true,
		SupportsDiskRateLimit:  true,
	}
}

// DestroyVM implements hypervisor.Hypervisor.
func (h *Hypervisor) DestroyVM(ctx context.Context) error {
	if _, err := h.client.DeleteVMWithResponse(ctx); err != nil {
		return fmt.Errorf("destroy vm: %w", err)
	}
	return nil
}

// ShutdownVM implements hypervisor.Hypervisor by pressing the guest's ACPI
// power button.
func (h *Hypervisor) ShutdownVM(ctx context.Context) error {
	if _, err := h.client.PowerButtonVMWithResponse(ctx); err != nil {
		return fmt.Errorf("press power button: %w", err)
	}
	return nil
}

// Shutdown implements hypervisor.Hypervisor.
func (h *Hypervisor) Shutdown(ctx context.Context) error {
	if _, err := h.client.ShutdownVMMWithResponse(ctx); err != nil {
		return fmt.Errorf("shut down hypervisor: %w", err)
	}
	return nil
}

// VMInfo implements hypervisor.Hypervisor. A VMM with no guest reports
// VMStateStopped.
func (h *Hypervisor) VMInfo(ctx context.Context) (*hypervisor.VMInfo, error) {
	resp, err := h.client.GetVmInfoWithResponse(ctx)
	// Cloud Hypervisor answers 500 when no guest is configured, such as
	// after DestroyVM.
	var statusErr *statusError
	if errors.As(err, &statusErr) && statusErr.code == http.StatusInternalServerError {
		return &hypervisor.VMInfo{State: hypervisor.VMStateStopped}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get vm info: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("get vm info: status %d: %s", resp.StatusCode(), resp.Body)
	}

	var state hypervisor.VMState
	switch resp.JSON200.State {
	case Created, Shutdown:
		// Created is a guest not yet booted and Shutdown one that has shut
		// down with its VMM still alive: neither is running.
		state = hypervisor.VMStateStopped
	case Running:
		state = hypervisor.VMStateRunning
	case Paused:
		state = hypervisor.VMStatePaused
	default:
		return nil, fmt.Errorf("unknown vm state %q", resp.JSON200.State)
	}

	return &hypervisor.VMInfo{State: state, MemoryBytes: resp.JSON200.MemoryActualSize}, nil
}

// PauseVM implements hypervisor.Hypervisor.
func (h *Hypervisor) PauseVM(ctx context.Context) error {
	if _, err := h.client.PauseVMWithResponse(ctx); err != nil {
		return fmt.Errorf("pause vm: %w", err)
	}
	return nil
}

// ResumeVM implements hypervisor.Hypervisor.
func (h *Hypervisor) ResumeVM(ctx context.Context) error {
	if _, err := h.client.ResumeVMWithResponse(ctx); err != nil {
		return fmt.Errorf("resume vm: %w", err)
	}
	return nil
}

// SnapshotVM implements hypervisor.Hypervisor.
func (h *Hypervisor) SnapshotVM(ctx context.Context, destPath string) error {
	if _, err := h.client.PutVmSnapshotWithResponse(ctx, VmSnapshotConfig{DestinationUrl: ptr("file://" + destPath)}); err != nil {
		return fmt.Errorf("snapshot vm: %w", err)
	}
	return nil
}

// ResizeVMCPU implements hypervisor.Hypervisor.
func (h *Hypervisor) ResizeVMCPU(ctx context.Context, count int) error {
	if _, err := h.client.PutVmResizeWithResponse(ctx, VmResize{DesiredVcpus: &count}); err != nil {
		return fmt.Errorf("resize cpu: %w", err)
	}
	return nil
}

// ResizeVMMemory implements hypervisor.Hypervisor. Cloud Hypervisor reports
// the memory asked for, not what the guest has plugged, so it returns as
// soon as the request is taken. It refuses memory below the boot memory,
// which Cloud Hypervisor would ignore.
func (h *Hypervisor) ResizeVMMemory(ctx context.Context, bytes int64) error {
	resp, err := h.client.GetVmInfoWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("resize memory: %w", err)
	}
	if resp.JSON200 == nil || resp.JSON200.Config.Memory == nil {
		return fmt.Errorf("resize memory: no memory configuration: status %d", resp.StatusCode())
	}
	memory := resp.JSON200.Config.Memory

	hotplugBytes := int64(0)
	if memory.HotplugSize != nil {
		hotplugBytes = *memory.HotplugSize
	}
	if err := hypervisor.CheckMemoryResize(bytes, memory.Size, hotplugBytes); err != nil {
		return err
	}

	if _, err := h.client.PutVmResizeWithResponse(ctx, VmResize{DesiredRam: &bytes}); err != nil {
		return fmt.Errorf("resize memory: %w", err)
	}
	return nil
}

// statusCheckingClient is an HTTP client that turns a response with a
// failure status into a statusError, so every API call reports failure
// through its error alone, and bounds a request whose context has no
// deadline by apiTimeout.
type statusCheckingClient struct {
	*http.Client
}

// Do sends req, returning a statusError for a response with a status other
// than 2xx. The body is read before Do returns, while the request's
// timeout still allows it.
func (c statusCheckingClient) Do(req *http.Request) (*http.Response, error) {
	if _, ok := req.Context().Deadline(); !ok {
		ctx, cancel := context.WithTimeout(req.Context(), apiTimeout)
		defer cancel()
		req = req.WithContext(ctx)
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &statusError{code: resp.StatusCode, body: body}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// statusError is a failure status the VMM answered a request with, along
// with the body explaining it.
type statusError struct {
	code int
	body []byte
}

func (e *statusError) Error() string {
	return fmt.Sprintf("status %d: %s", e.code, e.body)
}
