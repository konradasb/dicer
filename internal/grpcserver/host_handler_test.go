// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcserver

import (
	"context"
	"errors"
	"iter"
	"net/netip"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/konradasb/dicer/internal/doctor"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/token"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// errFakeStarter is what the starter below returns for everything but its
// version, which is all these tests ask of it.
var errFakeStarter = errors.New("fake starter")

// fakeStarter is a hypervisor.Starter that only knows its version.
type fakeStarter struct{ version string }

func (f fakeStarter) Version() string           { return f.version }
func (f fakeStarter) DefaultKernelArgs() string { return "" }
func (f fakeStarter) PowerOffEndsVM() bool      { return true }

func (f fakeStarter) StartVM(
	context.Context, string, hypervisor.VMSpec,
) (*process.Process, hypervisor.Hypervisor, error) {
	return nil, nil, errFakeStarter
}

func (f fakeStarter) RestoreVM(
	context.Context, string, string, hypervisor.RestoreSpec,
) (*process.Process, hypervisor.Hypervisor, error) {
	return nil, nil, errFakeStarter
}

func (f fakeStarter) Connect(string) (hypervisor.Hypervisor, error) {
	return nil, errFakeStarter
}

func TestHypervisorInfosPutTheDefaultFirst(t *testing.T) {
	h := &hostHandler{starters: map[hypervisor.Type][]hypervisor.Starter{
		hypervisor.TypeFirecracker:     {fakeStarter{version: "v1.17.0"}},
		hypervisor.TypeCloudHypervisor: {fakeStarter{version: "v49.0.0"}, fakeStarter{version: "v48.0.0"}},
	}}

	got := h.hypervisorInfos()
	if len(got) != 2 {
		t.Fatalf("got %d hypervisors, want 2", len(got))
	}

	// The default hypervisor comes first whatever order the map iterates in.
	if got[0].GetType() != dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR || !got[0].GetIsDefault() {
		t.Errorf("first entry = %+v, want cloud-hypervisor as the default", got[0])
	}
	if want := []string{"v49.0.0", "v48.0.0"}; len(got[0].GetVersions()) != 2 ||
		got[0].GetVersions()[0] != want[0] || got[0].GetVersions()[1] != want[1] {
		t.Errorf("versions = %v, want %v with the default first", got[0].GetVersions(), want)
	}
	if got[1].GetType() != dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER || got[1].GetIsDefault() {
		t.Errorf("second entry = %+v, want firecracker, not the default", got[1])
	}
}

// TestHypervisorInfosDeprecateAllButTheDefault covers the deprecation
// policy: every version but a hypervisor's default is deprecated.
func TestHypervisorInfosDeprecateAllButTheDefault(t *testing.T) {
	h := &hostHandler{starters: map[hypervisor.Type][]hypervisor.Starter{
		hypervisor.TypeCloudHypervisor: {
			fakeStarter{version: "v53.0.0"}, fakeStarter{version: "v49.0.0"}, fakeStarter{version: "v48.0.0"},
		},
		hypervisor.TypeFirecracker: {fakeStarter{version: "v1.17.0"}},
	}}

	got := h.hypervisorInfos()
	if len(got) != 2 {
		t.Fatalf("got %d hypervisors, want 2", len(got))
	}
	if got, want := got[0].GetDeprecatedVersions(), []string{"v49.0.0", "v48.0.0"}; !slices.Equal(got, want) {
		t.Errorf("cloud-hypervisor's deprecated versions = %v, want %v", got, want)
	}
	if got := got[1].GetDeprecatedVersions(); len(got) != 0 {
		t.Errorf("firecracker's deprecated versions = %v, want none", got)
	}
}

// TestHypervisorInfosOmitMissingDrivers covers a daemon that could not build
// a starter: what it cannot start must not be advertised.
func TestHypervisorInfosOmitMissingDrivers(t *testing.T) {
	h := &hostHandler{starters: map[hypervisor.Type][]hypervisor.Starter{
		hypervisor.TypeCloudHypervisor: {fakeStarter{version: "v49.0.0"}},
	}}

	got := h.hypervisorInfos()
	if len(got) != 1 || got[0].GetType() != dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR {
		t.Errorf("hypervisorInfos() = %+v, want only cloud-hypervisor", got)
	}
}

// TestListenerAddressesOfAListenerOnAllAddressesAreTheHosts checks that a
// client is told addresses it can connect to, not [::]:9000.
func TestListenerAddressesOfAListenerOnAllAddressesAreTheHosts(t *testing.T) {
	hostAddresses := func() ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.10.0.101"), netip.MustParseAddr("2001:db8::10")}, nil
	}
	failing := func() ([]netip.Addr, error) { return nil, errors.New("no interfaces") }

	tests := []struct {
		name          string
		listenAddress string
		hostAddresses func() ([]netip.Addr, error)
		want          []string
	}{
		{"not served over TCP", "", hostAddresses, nil},
		{"one address", "192.0.2.1:7443", hostAddresses, []string{"192.0.2.1:7443"}},
		{"all IPv4 addresses", "0.0.0.0:7443", hostAddresses, []string{"10.10.0.101:7443", "[2001:db8::10]:7443"}},
		{"all addresses", "[::]:9000", hostAddresses, []string{"10.10.0.101:9000", "[2001:db8::10]:9000"}},
		{"the host's unknown", "[::]:9000", nil, []string{"[::]:9000"}},
		{"the host's unreadable", "[::]:9000", failing, []string{"[::]:9000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &hostHandler{listenAddress: tt.listenAddress, hostAddresses: tt.hostAddresses}
			if got := h.listenerAddresses(); !slices.Equal(got, tt.want) {
				t.Errorf("listenerAddresses = %v, want %v", got, tt.want)
			}
		})
	}
}

// checkHostStream is a CheckHost stream that keeps what is sent.
type checkHostStream struct {
	grpc.ServerStream

	ctx  context.Context
	sent []*dicerdv1.HostCheckResult
}

func (s *checkHostStream) Context() context.Context { return s.ctx }

func (s *checkHostStream) Send(r *dicerdv1.HostCheckResult) error {
	s.sent = append(s.sent, r)
	return nil
}

func TestCheckHostSendsEachResult(t *testing.T) {
	var got doctor.Options
	h := &hostHandler{starters: carriesFirecracker(), checkHost: func(_ context.Context, opts doctor.Options) iter.Seq[doctor.Result] {
		got = opts
		return slices.Values([]doctor.Result{
			{Group: doctor.GroupHost, Name: "kvm", Status: doctor.StatusOK, Detail: "/dev/kvm is usable"},
			{Group: doctor.GroupHost, Name: "disk", Status: doctor.StatusWarning, Detail: "1 GiB free", Hint: "free some space"},
			{
				Group: doctor.GroupInstances, Name: "firecracker", Status: doctor.StatusFailed,
				Detail: "ended without running its command", Console: []string{"Kernel panic"},
			},
		})
	}}

	stream := &checkHostStream{ctx: t.Context()}
	req := &dicerdv1.CheckHostRequest{
		TestInstance: true, TestInstanceImage: "busybox", TestInstanceTimeout: durationpb.New(time.Minute),
		TestInstanceHypervisor: dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER, TestInstanceHypervisorVersion: "v1.17.0",
		KeepFailedTestInstance: true,
	}
	if err := h.CheckHost(req, stream); err != nil {
		t.Fatal(err)
	}

	wantOpts := doctor.Options{
		TestInstance: true, TestInstanceImage: "busybox", TestInstanceHypervisor: hypervisor.TypeFirecracker,
		TestInstanceHypervisorVersion: "v1.17.0", TestInstanceTimeout: time.Minute, KeepFailedTestInstance: true,
	}
	if got != wantOpts {
		t.Errorf("options = %+v, want %+v", got, wantOpts)
	}
	want := []*dicerdv1.HostCheckResult{
		{
			Group: dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_HOST, Name: "kvm",
			Status: dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_OK, Detail: "/dev/kvm is usable",
		},
		{
			Group: dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_HOST, Name: "disk",
			Status: dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_WARNING, Detail: "1 GiB free", Hint: "free some space",
		},
		{
			Group: dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_INSTANCES, Name: "firecracker",
			Status: dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_FAILED, Detail: "ended without running its command",
			Console: []string{"Kernel panic"},
		},
	}
	if !slices.EqualFunc(stream.sent, want, func(a, b *dicerdv1.HostCheckResult) bool {
		return proto.Equal(a, b)
	}) {
		t.Errorf("sent = %v, want %v", stream.sent, want)
	}
}

func TestCheckHostWithoutADoctorSendsNothing(t *testing.T) {
	stream := &checkHostStream{ctx: t.Context()}
	if err := (&hostHandler{}).CheckHost(&dicerdv1.CheckHostRequest{}, stream); err != nil || len(stream.sent) != 0 {
		t.Errorf("CheckHost without a doctor = %v, sent %v; want nothing sent", err, stream.sent)
	}
}

func TestCheckHostRefusesInvalidOptions(t *testing.T) {
	stream := &checkHostStream{ctx: t.Context()}
	req := &dicerdv1.CheckHostRequest{TestInstance: true, TestInstanceImage: "Not An Image"}
	if err := (&hostHandler{}).CheckHost(req, stream); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("CheckHost with an invalid image = %v, want ErrInvalidArgument", err)
	}
}

// carriesFirecracker returns the starters of a host that carries
// Firecracker, and Cloud Hypervisor, the default.
func carriesFirecracker() map[hypervisor.Type][]hypervisor.Starter {
	return map[hypervisor.Type][]hypervisor.Starter{
		hypervisor.TypeCloudHypervisor: {fakeStarter{version: "v53.0.0"}},
		hypervisor.TypeFirecracker:     {fakeStarter{version: "v1.17.0"}},
	}
}

func TestCheckHostRefusesAHypervisorOrVersionTheHostLacks(t *testing.T) {
	h := &hostHandler{starters: map[hypervisor.Type][]hypervisor.Starter{
		hypervisor.TypeCloudHypervisor: {fakeStarter{version: "v53.0.0"}},
	}}
	req := &dicerdv1.CheckHostRequest{
		TestInstance: true, TestInstanceHypervisor: dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER,
	}
	if err := h.CheckHost(req, &checkHostStream{ctx: t.Context()}); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("CheckHost on a hypervisor the host lacks = %v, want ErrInvalidArgument", err)
	}

	versioned := &dicerdv1.CheckHostRequest{TestInstance: true, TestInstanceHypervisorVersion: "v52.0.0"}
	if err := h.CheckHost(versioned, &checkHostStream{ctx: t.Context()}); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("CheckHost on a hypervisor version the host lacks = %v, want ErrInvalidArgument", err)
	}
	versioned.TestInstanceHypervisorVersion = "v53.0.0"
	if err := h.CheckHost(versioned, &checkHostStream{ctx: t.Context()}); err != nil {
		t.Errorf("CheckHost on a hypervisor version the host has = %v", err)
	}

	// Without a test instance, the hypervisor does not matter.
	req.TestInstance = false
	if err := h.CheckHost(req, &checkHostStream{ctx: t.Context()}); err != nil {
		t.Errorf("CheckHost without a test instance = %v", err)
	}
}

// TestBootingATestInstanceNeedsInstancesWrite checks that a token without
// instances:write may check the host, but not boot a test instance, and
// that a call over the socket, which has no token, may do both.
func TestBootingATestInstanceNeedsInstancesWrite(t *testing.T) {
	tests := []struct {
		name         string
		token        *token.Token
		testInstance bool
		wantErr      error
	}{
		{name: "the socket", testInstance: true},
		{name: "a read-only token checking the host", token: &token.Token{Name: "ci"}},
		{
			name: "a read-only token booting a test instance", token: &token.Token{Name: "ci"},
			testInstance: true, wantErr: errdefs.ErrPermissionDenied,
		},
		{
			name: "a token with instances:write", testInstance: true,
			token: &token.Token{Name: "ops", Scopes: []token.Scope{token.ScopeInstancesWrite}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			if tt.token != nil {
				ctx = context.WithValue(ctx, tokenKey{}, *tt.token)
			}
			h := &hostHandler{starters: carriesFirecracker()}
			err := h.CheckHost(&dicerdv1.CheckHostRequest{TestInstance: tt.testInstance}, &checkHostStream{ctx: ctx})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("CheckHost = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
