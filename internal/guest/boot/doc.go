// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package boot is the guest's PID 1.
//
// It runs inside the virtual machine, not on the host: it mounts the root
// filesystem, applies the guest.Config handed over on the config disk, and
// then either runs the workload as PID 1 of its own PID namespace,
// supervising it, or hands the machine's PID 1 to systemd.
package boot
