// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package boot is the guest's PID 1. It mounts the root filesystem, applies
// the guest.Config from the config disk, and then either supervises the
// workload in its own PID namespace or hands PID 1 to systemd.
package boot
