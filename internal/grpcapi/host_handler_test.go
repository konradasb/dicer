// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"errors"
	"testing"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	"github.com/dicer-sh/dicer/internal/process"
)

// errFakeStarter is what the starter below returns for everything but its
// version, which is all these tests ask of it.
var errFakeStarter = errors.New("fake starter")

// fakeStarter is a hypervisor.Starter that only knows its version.
type fakeStarter struct{ version string }

func (f fakeStarter) Version() string         { return f.version }
func (f fakeStarter) DefaultBootArgs() string { return "" }
func (f fakeStarter) PowerOffEndsVM() bool    { return true }

func (f fakeStarter) StartVM(
	context.Context, string, hypervisor.VirtualMachine,
) (*process.Process, hypervisor.Hypervisor, error) {
	return nil, nil, errFakeStarter
}

func (f fakeStarter) RestoreVM(
	context.Context, string, string, hypervisor.ConsoleConfig,
) (*process.Process, hypervisor.Hypervisor, error) {
	return nil, nil, errFakeStarter
}

func (f fakeStarter) Connect(string) (hypervisor.Hypervisor, error) {
	return nil, errFakeStarter
}

func TestHypervisorInfo(t *testing.T) {
	h := &hostHandler{hypervisors: map[dicer.HypervisorType][]hypervisor.Starter{
		dicer.HypervisorFirecracker:     {fakeStarter{version: "v1.17.0"}},
		dicer.HypervisorCloudHypervisor: {fakeStarter{version: "v49.0.0"}, fakeStarter{version: "v48.0.0"}},
	}}

	got := h.hypervisorInfo()
	if len(got) != 2 {
		t.Fatalf("got %d hypervisors, want 2", len(got))
	}

	// The default hypervisor comes first whatever order the map iterates in.
	if got[0].GetType() != string(dicer.HypervisorCloudHypervisor) || !got[0].GetIsDefault() {
		t.Errorf("first entry = %+v, want cloud-hypervisor as the default", got[0])
	}
	if want := []string{"v49.0.0", "v48.0.0"}; len(got[0].GetVersions()) != 2 ||
		got[0].GetVersions()[0] != want[0] || got[0].GetVersions()[1] != want[1] {
		t.Errorf("versions = %v, want %v with the default first", got[0].GetVersions(), want)
	}
	if got[1].GetType() != string(dicer.HypervisorFirecracker) || got[1].GetIsDefault() {
		t.Errorf("second entry = %+v, want firecracker, not the default", got[1])
	}
}

// TestHypervisorInfoOmitsMissingDrivers covers a daemon that could not build
// a starter: what it cannot start must not be advertised.
func TestHypervisorInfoOmitsMissingDrivers(t *testing.T) {
	h := &hostHandler{hypervisors: map[dicer.HypervisorType][]hypervisor.Starter{
		dicer.HypervisorCloudHypervisor: {fakeStarter{version: "v49.0.0"}},
	}}

	got := h.hypervisorInfo()
	if len(got) != 1 || got[0].GetType() != string(dicer.HypervisorCloudHypervisor) {
		t.Errorf("hypervisorInfo() = %+v, want only cloud-hypervisor", got)
	}
}
