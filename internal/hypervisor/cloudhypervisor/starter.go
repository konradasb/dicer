// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gvisor.dev/gvisor/pkg/cleanup"

	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/process"
)

// Starter implements hypervisor.Starter for Cloud Hypervisor.
type Starter struct {
	binaryPath string
	version    string

	// restoresMemoryOnDemand reports whether RestoreVM restores the guest's
	// memory on demand.
	restoresMemoryOnDemand bool
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

	return &Starter{
		binaryPath:             binaryPath,
		version:                string(v),
		restoresMemoryOnDemand: v.restoresMemoryOnDemand() && hasUserfaultfd(),
	}, nil
}

// hasUserfaultfd reports whether the host's kernel has userfaultfd. Cloud
// Hypervisor needs it to restore memory on demand, and fails the restore if
// it is missing. The sysctl exists only in a kernel built with userfaultfd.
func hasUserfaultfd() bool {
	_, err := os.Stat("/proc/sys/vm/unprivileged_userfaultfd")
	return err == nil
}

// Version returns the Cloud Hypervisor binary version string.
func (s *Starter) Version() string { return s.version }

// DefaultKernelArgs returns the kernel command line Cloud Hypervisor guests
// need: output on the serial port, which is the console, and a reboot on
// panic.
func (s *Starter) DefaultKernelArgs() string {
	return "console=ttyS0 reboot=k panic=1"
}

// PowerOffEndsVM is true: Cloud Hypervisor exits when its guest powers off
// over ACPI. A reset, by contrast, reboots the guest in place.
func (s *Starter) PowerOffEndsVM() bool { return true }

// StartVM launches Cloud Hypervisor, configures the guest and boots it. It
// returns the VMM process and a client for controlling it.
func (s *Starter) StartVM(
	ctx context.Context, socketPath string, spec hypervisor.VMSpec,
) (*process.Process, hypervisor.Hypervisor, error) {
	proc, hv, cu, err := s.start(ctx, socketPath)
	if err != nil {
		return nil, nil, err
	}
	defer cu.Clean()

	if _, err := hv.client.CreateVMWithResponse(ctx, vmConfig(spec)); err != nil {
		return nil, nil, fmt.Errorf("create vm: %w", err)
	}

	if _, err := hv.client.BootVMWithResponse(ctx); err != nil {
		return nil, nil, fmt.Errorf("boot vm: %w", err)
	}

	cu.Release()
	return proc, hv, nil
}

// restoreDir is the directory, beside the API socket, a snapshot with
// another TAP device is staged in to be restored from.
const restoreDir = "restore"

// RestoreVM launches Cloud Hypervisor and restores a guest from a snapshot,
// leaving it paused. The console is unused: the snapshot carries it.
//
// If the version and the host allow it, it restores the guest's memory on
// demand. The guest resumes at once, and Cloud Hypervisor restores each page
// from the snapshot when the guest first uses it, and the rest in the
// background. Otherwise it restores all of the memory before returning.
func (s *Starter) RestoreVM(
	ctx context.Context, socketPath string, snapshotPath string, spec hypervisor.RestoreSpec,
) (*process.Process, hypervisor.Hypervisor, error) {
	if spec.TAPDevice != "" {
		dir := filepath.Join(filepath.Dir(socketPath), restoreDir)
		if err := stageRestore(snapshotPath, dir, spec.TAPDevice); err != nil {
			return nil, nil, fmt.Errorf("stage snapshot: %w", err)
		}
		snapshotPath = dir
	}

	proc, hv, cu, err := s.start(ctx, socketPath)
	if err != nil {
		return nil, nil, err
	}
	defer cu.Clean()

	config := RestoreConfig{SourceUrl: "file://" + snapshotPath, Prefault: ptr(false)}
	if s.restoresMemoryOnDemand {
		config.MemoryRestoreMode = ptr(OnDemand)
	}
	if _, err := hv.client.PutVmRestoreWithResponse(ctx, config); err != nil {
		return nil, nil, fmt.Errorf("restore snapshot: %w", err)
	}

	cu.Release()
	return proc, hv, nil
}

// Connect returns a client for an already-running Cloud Hypervisor VMM.
func (s *Starter) Connect(socketPath string) (hypervisor.Hypervisor, error) {
	return NewHypervisor(socketPath), nil
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

	return proc, NewHypervisor(socketPath), cleanup.Make(proc.Terminate), nil
}
