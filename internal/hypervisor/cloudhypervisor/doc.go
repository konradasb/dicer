// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package cloudhypervisor implements the hypervisor interfaces on top of
// Cloud Hypervisor.
//
// It embeds the VMM binaries it supports, launches them detached so a guest
// outlives the daemon, and drives each one over its API socket. The generated
// OpenAPI client lives in client_gen.go and is not hand-edited.
package cloudhypervisor
