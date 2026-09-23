// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"fmt"

	"github.com/dicer-sh/dicer/internal/guest"
	"github.com/dicer-sh/dicer/internal/process"
)

// Exit is how an instance's guest ended, as far as the host can tell.
//
// Only the guest knows whether it meant to end, so an end is clean only if
// it said so: the workload exited 0, or systemd powered the guest off. Every
// other end -- a non-zero exit, a reset, a VMM that died, a guest that went
// without a word -- is a failure. A VMM's own exit status cannot stand in for
// the guest's: a VMM that ends on a reset exits cleanly whether the guest
// meant to reset or panicked, and one adopted from a previous daemon has no
// exit status to read at all.
type Exit struct {
	// Code is the exit code the guest reported, if it reported one.
	Code *int

	// Failure is why the end was not clean, or nil if it was.
	Failure error
}

// Clean reports whether the guest ended because it meant to.
func (e Exit) Clean() bool { return e.Failure == nil }

// failedExit is the Exit of an instance that ended because of cause, with no
// word from the guest: its VMM was lost, or it never started.
func failedExit(cause error) Exit { return Exit{Failure: cause} }

// readExit reads how an instance's guest ended from what it reported on its
// status disk, and why its VMM exited: vmmErr is process.Process.Err.
func (m *Manager) readExit(instanceID string, vmmErr error) Exit {
	st, err := readStatusDisk(m.statusDiskPath(instanceID))
	if err != nil {
		m.logger.Warn("cannot read how the guest ended", "instance_id", instanceID, "error", err)
	}
	return classifyExit(st, vmmErr)
}

// classifyExit is readExit once the status disk has been read. A status disk
// that could not be read is the zero Status: the guest said nothing.
func classifyExit(st guest.Status, vmmErr error) Exit {
	if st.ExitCode != nil {
		code := *st.ExitCode
		if code == 0 {
			return Exit{Code: &code}
		}
		return Exit{Code: &code, Failure: fmt.Errorf("exit code %d", code)}
	}

	switch {
	case st.Boots > 1:
		return failedExit(errors.New("guest reset (kernel panic or reboot)"))
	case errors.Is(vmmErr, process.ErrExitStatusUnknown):
		// A VMM adopted from a previous daemon: whether it crashed or its
		// guest ended quietly, nothing says which.
		return failedExit(errors.New("hypervisor exited, no exit code reported"))
	case vmmErr != nil:
		return failedExit(fmt.Errorf("hypervisor exited unexpectedly: %w", vmmErr))
	default:
		return failedExit(errors.New("guest ended, no exit code reported"))
	}
}
