// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"fmt"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/process"
)

// Exit is how an instance's guest ended. An end is clean only if the guest
// reported exit code 0; anything else is a failure.
type Exit struct {
	// Code is the exit code the guest reported, if it reported one.
	Code *int

	// Failure is why the end was not clean, or nil if it was.
	Failure error
}

// Clean reports whether the guest ended because it meant to.
func (e Exit) Clean() bool { return e.Failure == nil }

// failedExit is the Exit of an instance that ended because of cause.
func failedExit(cause error) Exit { return Exit{Failure: cause} }

// readExit reads how an instance's guest ended from its status disk. vmmErr
// is the VMM's process.Process.Err.
func (m *Manager) readExit(instanceID string, vmmErr error) Exit {
	st, err := readStatusDisk(m.statusDiskPath(instanceID))
	if err != nil {
		m.logger.Warn("cannot read how the guest ended", "instance_id", instanceID, "error", err)
	}
	return classifyExit(st, vmmErr)
}

// classifyExit decides an Exit from the guest's status and the VMM's error.
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
		return failedExit(errors.New("hypervisor exited, no exit code reported"))
	case vmmErr != nil:
		return failedExit(fmt.Errorf("hypervisor exited unexpectedly: %w", vmmErr))
	default:
		return failedExit(errors.New("guest ended, no exit code reported"))
	}
}
