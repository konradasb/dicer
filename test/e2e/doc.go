// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

// Package e2e boots real virtual machines on a real host.
//
// Everything below this package can be tested without KVM, and is. The parts
// that cannot -- the hypervisor, host networking and the guest boot sequence
// -- are the parts that fail in ways users notice, so these tests drive a
// Linux box over SSH and assert on what actually happened there.
//
// They are behind the `e2e` build tag, so `go test ./...` never picks them up
// and CI stays a machine that needs no KVM. Run them with:
//
//	make test-e2e DICER_E2E_HOST=10.10.0.101
//
// Tests are grouped into files by the resource they exercise, matching the
// nouns the API exposes: instance, image, network, volume and snapshot, plus
// daemon for behaviour that belongs to no resource and metrics for the
// endpoint. Things that are not resources go with the resource they are part
// of -- a hypervisor is an instance's attribute and exec is an instance's
// verb, so both are in instance_test.go.
//
// Each of those files owns the helpers for its own resource, so that how to
// create an instance sits beside the tests that create one. Only what every
// resource needs is shared: e2e_test.go builds the environment, remote_test.go
// reaches the host over SSH, and dicer_test.go runs CLI commands and decodes
// what they print.
//
// The daemon under test is installed entirely under its own prefix -- its own
// data directory, runtime directory, socket and systemd unit -- so it cannot
// disturb a real Dicer installation on the same host, and so a run that is
// killed rather than torn down leaves a self-contained mess that the next one
// clears up.
package e2e
