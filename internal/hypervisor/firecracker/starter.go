// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gvisor.dev/gvisor/pkg/cleanup"

	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/process"
)

// Starter implements hypervisor.Starter for Firecracker.
type Starter struct {
	binaryPath string
	version    string
}

var _ hypervisor.Starter = (*Starter)(nil)

// NewStarter creates a Starter for a Firecracker binary. It invokes the
// binary once to parse and validate its version.
func NewStarter(binaryPath string) (*Starter, error) {
	if _, err := os.Stat(binaryPath); err != nil {
		return nil, err
	}

	v, err := parseVersion(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("firecracker: %w", err)
	}

	return &Starter{binaryPath: binaryPath, version: string(v)}, nil
}

// Version returns the Firecracker binary version string.
func (s *Starter) Version() string { return s.version }

// DefaultKernelArgs returns the kernel command line Firecracker guests need:
// output on the serial console, no PCI bus, and a reset on panic so that a
// wedged guest ends its VMM rather than lingering.
func (s *Starter) DefaultKernelArgs() string {
	return "console=ttyS0 reboot=k panic=1 pci=off"
}

// PowerOffEndsVM is false: Firecracker has no ACPI power button, so a guest
// that powers off only halts. It exits when the guest resets.
func (s *Starter) PowerOffEndsVM() bool { return false }

// StartVM launches Firecracker, configures the guest and boots it. It
// returns the VMM process and a client for controlling it.
func (s *Starter) StartVM(
	ctx context.Context, socketPath string, spec hypervisor.VMSpec,
) (*process.Process, hypervisor.Hypervisor, error) {
	// Translate before launching anything: a specification Firecracker
	// cannot honour should fail without leaving a process behind.
	setup, err := newSetup(spec)
	if err != nil {
		return nil, nil, err
	}

	proc, hv, cu, err := s.start(ctx, socketPath)
	if err != nil {
		return nil, nil, err
	}
	defer cu.Clean()

	if err := setup.apply(ctx, hv.client); err != nil {
		return nil, nil, fmt.Errorf("configure vm: %w", err)
	}

	if err := hv.client.put(ctx, "/actions", instanceAction{ActionType: actionInstanceStart}); err != nil {
		return nil, nil, fmt.Errorf("boot vm: %w", err)
	}

	cu.Release()
	return proc, hv, nil
}

// RestoreVM launches Firecracker and restores a guest from a snapshot
// written by SnapshotVM. The guest is left paused; call ResumeVM.
func (s *Starter) RestoreVM(
	ctx context.Context, socketPath string, snapshotPath string, spec hypervisor.RestoreSpec,
) (*process.Process, hypervisor.Hypervisor, error) {
	proc, hv, cu, err := s.start(ctx, socketPath)
	if err != nil {
		return nil, nil, err
	}
	defer cu.Clean()

	// Where the console is written is not part of a Firecracker snapshot,
	// so it has to be set again -- before the snapshot is loaded, while the
	// VMM will still accept configuration.
	if spec.Console.Path != "" {
		if err := hv.client.put(ctx, "/serial", serialDevice{SerialOutPath: spec.Console.Path}); err != nil {
			return nil, nil, fmt.Errorf("configure serial console: %w", err)
		}
	}

	load := snapshotLoad{
		SnapshotPath: filepath.Join(snapshotPath, snapshotStateFile),
		MemBackend: memoryBackend{
			BackendType: "File",
			BackendPath: filepath.Join(snapshotPath, snapshotMemoryFile),
		},
		ResumeVM: false,
	}
	if spec.TAPDevice != "" {
		// The guest has one interface, which newSetup named eth0.
		load.NetworkOverrides = []networkOverride{{IfaceID: "eth0", HostDevName: spec.TAPDevice}}
	}
	err = hv.client.put(ctx, "/snapshot/load", load)
	if err != nil {
		return nil, nil, fmt.Errorf("restore snapshot: %w", err)
	}

	cu.Release()
	return proc, hv, nil
}

// Connect returns a client for an already-running Firecracker VMM.
func (s *Starter) Connect(socketPath string) (hypervisor.Hypervisor, error) {
	return NewHypervisor(socketPath), nil
}

// start launches the VMM process and returns a client for it, along with a
// cleanup that kills the process until the caller releases it.
func (s *Starter) start(
	ctx context.Context, socketPath string,
) (*process.Process, *Hypervisor, cleanup.Cleanup, error) {
	proc, err := hypervisor.StartProcess(ctx, socketPath, s.binaryPath, "--api-sock", socketPath)
	if err != nil {
		return nil, nil, cleanup.Cleanup{}, fmt.Errorf("start process: %w", err)
	}

	return proc, NewHypervisor(socketPath), cleanup.Make(proc.Terminate), nil
}
