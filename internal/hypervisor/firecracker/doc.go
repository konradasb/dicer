// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package firecracker implements the hypervisor interfaces with Firecracker.
// It embeds the supported VMM binaries and drives them over their API socket
// using the hand-written types in api.go. Unsupported operations return
// errors.ErrUnsupported, as Capabilities reports.
package firecracker
