// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"gvisor.dev/gvisor/pkg/cleanup"

	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/process"
)

// Starter implements hypervisor.Starter for Cloud Hypervisor.
type Starter struct {
	binaryPath string
	version    string
}

var _ hypervisor.Starter = (*Starter)(nil)

// NewStarter creates a Starter for a Cloud Hypervisor binary. It invokes the
// binary once to parse and validate its version.
func NewStarter(binaryPath string) (*Starter, error) {
	if _, err := os.Stat(binaryPath); err != nil {
		return nil, err
	}

	v, err := parseVersion(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("cloud-hypervisor: %w", err)
	}

	return &Starter{binaryPath: binaryPath, version: string(v)}, nil
}

// Version returns the Cloud Hypervisor binary version string.
func (s *Starter) Version() string { return s.version }

// DefaultBootArgs returns the kernel arguments required for Cloud Hypervisor
// with a virtio-serial (ttyS0) console.
func (s *Starter) DefaultBootArgs() string {
	return "console=ttyS0 reboot=k panic=1"
}

// PowerOffEndsVM is true: Cloud Hypervisor exits when its guest powers off
// over ACPI. A reset, by contrast, reboots the guest in place.
func (s *Starter) PowerOffEndsVM() bool { return true }

// StartVM launches Cloud Hypervisor, configures the guest and boots it. It
// returns the VMM process and a client for controlling it.
func (s *Starter) StartVM(
	ctx context.Context, socketPath string, spec hypervisor.VirtualMachine,
) (*process.Process, hypervisor.Hypervisor, error) {
	proc, hv, cu, err := s.start(ctx, socketPath)
	if err != nil {
		return nil, nil, err
	}
	defer cu.Clean()

	createResp, err := hv.client.CreateVMWithResponse(ctx, ToVMConfig(spec))
	if err != nil {
		return nil, nil, fmt.Errorf("create vm: %w", err)
	}
	if createResp.StatusCode() != http.StatusNoContent {
		return nil, nil, fmt.Errorf("create vm: status %d: %s", createResp.StatusCode(), createResp.Body)
	}

	bootResp, err := hv.client.BootVMWithResponse(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("boot vm: %w", err)
	}
	if bootResp.StatusCode() != http.StatusNoContent {
		return nil, nil, fmt.Errorf("boot vm: status %d: %s", bootResp.StatusCode(), bootResp.Body)
	}

	cu.Release()
	return proc, hv, nil
}

// RestoreVM launches Cloud Hypervisor and restores a guest from a snapshot,
// leaving it paused. The console is unused: the snapshot carries it.
func (s *Starter) RestoreVM(
	ctx context.Context, socketPath string, snapshotPath string, _ hypervisor.ConsoleConfig,
) (*process.Process, hypervisor.Hypervisor, error) {
	proc, hv, cu, err := s.start(ctx, socketPath)
	if err != nil {
		return nil, nil, err
	}
	defer cu.Clean()

	resp, err := hv.client.PutVmRestoreWithResponse(ctx, RestoreConfig{
		SourceUrl: "file://" + snapshotPath,
		Prefault:  ptr(false),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("restore snapshot: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return nil, nil, fmt.Errorf("restore snapshot: status %d: %s", resp.StatusCode(), resp.Body)
	}

	cu.Release()
	return proc, hv, nil
}

// Connect returns a client for an already-running Cloud Hypervisor VMM.
func (s *Starter) Connect(socketPath string) (hypervisor.Hypervisor, error) {
	return NewHypervisor(socketPath)
}

// start launches the VMM process and returns a client for it, along with a
// cleanup that kills the process until the caller releases it.
func (s *Starter) start(
	ctx context.Context, socketPath string,
) (*process.Process, *Hypervisor, cleanup.Cleanup, error) {
	proc, err := hypervisor.StartProcess(ctx, socketPath, s.binaryPath, "--api-socket", socketPath)
	if err != nil {
		return nil, nil, cleanup.Cleanup{}, fmt.Errorf("start process: %w", err)
	}
	cu := cleanup.Make(proc.Terminate)

	hv, err := NewHypervisor(socketPath)
	if err != nil {
		cu.Clean()
		return nil, nil, cleanup.Cleanup{}, err
	}

	return proc, hv, cu, nil
}
