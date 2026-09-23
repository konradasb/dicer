// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// The request and response bodies below follow the Firecracker API, as
// described by src/firecracker/swagger/firecracker.yaml in its repository.
// Only the fields Dicer sets or reads are declared.

type bootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args,omitempty"`
	InitrdPath      string `json:"initrd_path,omitempty"`
}

type machineConfig struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	SMT        bool `json:"smt"`
}

type drive struct {
	DriveID      string       `json:"drive_id"`
	PathOnHost   string       `json:"path_on_host"`
	IsRootDevice bool         `json:"is_root_device"`
	IsReadOnly   bool         `json:"is_read_only"`
	RateLimiter  *rateLimiter `json:"rate_limiter,omitempty"`
}

type rateLimiter struct {
	Bandwidth *tokenBucket `json:"bandwidth,omitempty"`
}

// tokenBucket holds Size tokens, refilled over RefillTime milliseconds,
// plus a OneTimeBurst spent before refilling starts.
type tokenBucket struct {
	Size         int64 `json:"size"`
	OneTimeBurst int64 `json:"one_time_burst,omitempty"`
	RefillTime   int64 `json:"refill_time"`
}

type networkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	GuestMAC    string `json:"guest_mac,omitempty"`
	MTU         int    `json:"mtu,omitempty"`
}

type vsock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type memoryHotplugConfig struct {
	TotalSizeMiB int `json:"total_size_mib"`
}

type memoryHotplugUpdate struct {
	RequestedSizeMiB int `json:"requested_size_mib"`
}

type memoryHotplugStatus struct {
	TotalSizeMiB     int `json:"total_size_mib"`
	PluggedSizeMiB   int `json:"plugged_size_mib"`
	RequestedSizeMiB int `json:"requested_size_mib"`
}

type serialDevice struct {
	SerialOutPath string `json:"serial_out_path"`
}

type instanceAction struct {
	ActionType string `json:"action_type"`
}

// The instance actions Dicer uses.
const (
	actionInstanceStart  = "InstanceStart"
	actionSendCtrlAltDel = "SendCtrlAltDel"
)

// vmState is the body of PATCH /vm.
type vmState struct {
	State string `json:"state"`
}

// The states PATCH /vm accepts.
const (
	vmPaused  = "Paused"
	vmResumed = "Resumed"
)

type instanceInfo struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	VMMVersion string `json:"vmm_version"`
	AppName    string `json:"app_name"`
}

// The states GET / reports.
const (
	instanceNotStarted = "Not started"
	instanceRunning    = "Running"
	instancePaused     = "Paused"
)

// vmConfig is the part of GET /vm/config that Dicer reads.
type vmConfig struct {
	MachineConfig machineConfig        `json:"machine-config"`
	MemoryHotplug *memoryHotplugConfig `json:"memory-hotplug,omitempty"`
}

type snapshotCreate struct {
	SnapshotType string `json:"snapshot_type"`
	SnapshotPath string `json:"snapshot_path"`
	MemFilePath  string `json:"mem_file_path"`
}

type snapshotLoad struct {
	SnapshotPath string        `json:"snapshot_path"`
	MemBackend   memoryBackend `json:"mem_backend"`
	ResumeVM     bool          `json:"resume_vm"`
}

type memoryBackend struct {
	BackendType string `json:"backend_type"`
	BackendPath string `json:"backend_path"`
}

// apiError is the body Firecracker returns with a failed request.
type apiError struct {
	FaultMessage string `json:"fault_message"`
}

// client speaks the Firecracker API over its Unix socket.
type client struct {
	http *http.Client
}

func newClient(socketPath string) *client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}

	return &client{http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

func (c *client) put(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodPut, path, body, nil)
}

func (c *client) patch(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

func (c *client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// do sends a request and decodes a successful response into out, if given.
// A failure carries Firecracker's own explanation of it.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		reqBody = bytes.NewReader(data)
	}

	// The host is ignored: the transport always dials the API socket.
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, reqBody)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		var apiErr apiError
		data, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(data, &apiErr) == nil && apiErr.FaultMessage != "" {
			return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, apiErr.FaultMessage)
		}
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(data))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}

	return nil
}
