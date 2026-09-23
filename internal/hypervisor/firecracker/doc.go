// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package firecracker implements the hypervisor interfaces on top of
// Firecracker.
//
// Like the Cloud Hypervisor driver, it embeds the VMM binaries it supports,
// launches them detached so a guest outlives the daemon, and drives each one
// over its API socket. Firecracker publishes a Swagger 2.0 description of
// that API, which oapi-codegen cannot read, so the small part of it Dicer
// uses is written out by hand in api.go.
//
// Firecracker is deliberately minimal, and some of what the abstraction
// offers has no equivalent: vCPU hotplug, CPU pinning and topology, PCI
// passthrough, and destroying a guest without ending the process. Those
// report errors.ErrUnsupported, and Capabilities says so up front.
package firecracker
