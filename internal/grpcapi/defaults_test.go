// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer"
)

func TestDefaultsFallBackToTheOnlyOne(t *testing.T) {
	_, definitions := newResourceServer(t)
	r := defaultResolver{definitions: definitions}

	// None yet: nothing to default to, and the error says how to get one.
	if _, err := r.resolveKernel(""); status.Code(err) != codes.FailedPrecondition ||
		!strings.Contains(err.Error(), "none has been imported") {
		t.Errorf("resolveKernel with no kernels = %v", err)
	}

	if err := definitions.CreateKernel(dicer.Kernel{ID: "k-1", Name: "k1"}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.resolveKernel(""); err != nil || got != "k1" {
		t.Errorf("resolveKernel with one kernel = %q, %v; want k1", got, err)
	}
	if got := r.kernel(); got != "k1" {
		t.Errorf("kernel() = %q, want k1", got)
	}

	// Two: ambiguous, so the error lists them.
	if err := definitions.CreateKernel(dicer.Kernel{ID: "k-2", Name: "k2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolveKernel(""); status.Code(err) != codes.InvalidArgument ||
		!strings.Contains(err.Error(), "k1, k2") || !strings.Contains(err.Error(), "defaults.kernel") {
		t.Errorf("resolveKernel with two kernels = %v", err)
	}

	// A name given is used as given; it is checked elsewhere.
	if got, err := r.resolveKernel("other"); err != nil || got != "other" {
		t.Errorf("resolveKernel(other) = %q, %v", got, err)
	}
}

func TestDefaultsConfiguredWin(t *testing.T) {
	_, definitions := newResourceServer(t)
	for _, n := range []string{"a", "b"} {
		if err := definitions.CreateNetwork(dicer.Network{
			ID: "n-" + n, Name: n, Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Bridge: "dicer-" + n,
		}); err != nil {
			t.Fatal(err)
		}
	}

	r := defaultResolver{definitions: definitions, configured: Defaults{Network: "b"}}
	if got, err := r.resolveNetwork(""); err != nil || got != "b" {
		t.Errorf("resolveNetwork = %q, %v; want the configured b", got, err)
	}
}
