// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import (
	"os"
	"syscall"
)

// ExitStatus is the exit status of a finished process as a shell reports
// it: its exit code, or 128 plus the number of the signal that killed it --
// 137 for the SIGKILL the OOM killer sends. Go reports a killed process's
// exit code as -1, which says only that it has none.
func ExitStatus(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// ShutdownSignal asks the guest's PID 1 to shut down in an orderly way: the
// signal systemd powers the machine off on (SIGRTMIN+4, as C libraries
// number it). dicer-init answers it the same way, by asking the workload to
// stop, so the agent sends it without needing to know which one is PID 1.
const ShutdownSignal = syscall.Signal(38)
