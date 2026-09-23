// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/vm"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

func TestVolumeMounts(t *testing.T) {
	definitions, err := filestore.NewManager(filestore.Config{
		DataDir: filepath.Join(t.TempDir(), "data"),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range vm.MaxVolumeMounts + 1 {
		name := fmt.Sprintf("v%d", i)
		if err := definitions.CreateVolume(dicer.Volume{ID: "id-" + name, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	h := &instanceHandler{definitions: definitions}

	got, err := h.volumeMounts([]*dicerdv1.VolumeMount{
		{VolumeName: "v0", MountPath: "/data/"},
		{VolumeName: "v1", MountPath: "/logs", AccessMode: string(dicer.AccessModeReadOnlyMany)},
	})
	if err != nil {
		t.Fatalf("volumeMounts: %v", err)
	}
	want := []dicer.VolumeMount{
		{VolumeName: "v0", MountPath: "/data", AccessMode: dicer.AccessModeReadWriteOnce},
		{VolumeName: "v1", MountPath: "/logs", AccessMode: dicer.AccessModeReadOnlyMany},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("volumeMounts = %v, want %v", got, want)
	}

	tooMany := make([]*dicerdv1.VolumeMount, vm.MaxVolumeMounts+1)
	for i := range tooMany {
		tooMany[i] = &dicerdv1.VolumeMount{VolumeName: fmt.Sprintf("v%d", i), MountPath: fmt.Sprintf("/v%d", i)}
	}

	tests := []struct {
		name string
		in   []*dicerdv1.VolumeMount
	}{
		{"unknown volume", []*dicerdv1.VolumeMount{{VolumeName: "nope", MountPath: "/data"}}},
		{"relative path", []*dicerdv1.VolumeMount{{VolumeName: "v0", MountPath: "data"}}},
		{"empty path", []*dicerdv1.VolumeMount{{VolumeName: "v0"}}},
		{"root", []*dicerdv1.VolumeMount{{VolumeName: "v0", MountPath: "/."}}},
		{"same volume twice", []*dicerdv1.VolumeMount{
			{VolumeName: "v0", MountPath: "/a"}, {VolumeName: "v0", MountPath: "/b"},
		}},
		{"same path twice", []*dicerdv1.VolumeMount{
			{VolumeName: "v0", MountPath: "/a"}, {VolumeName: "v1", MountPath: "/a/"},
		}},
		{"bad access mode", []*dicerdv1.VolumeMount{{VolumeName: "v0", MountPath: "/a", AccessMode: "rw"}}},
		{"too many", tooMany},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.volumeMounts(tt.in)
			wantCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestValidateCreateChecksHostname(t *testing.T) {
	req := &dicerdv1.CreateInstanceRequest{
		Name: "web", ImageRef: "busybox", Vcpus: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 30,
	}
	for _, hostname := range []string{"", "web", "web.example.com"} {
		req.Hostname = hostname
		if err := validateCreate(req); err != nil {
			t.Errorf("validateCreate(hostname %q) = %v", hostname, err)
		}
	}
	for _, hostname := range []string{"web_1", "-web", "has space"} {
		req.Hostname = hostname
		wantCode(t, validateCreate(req), codes.InvalidArgument)
	}
}

func TestPortMappingsCanonicalHostIP(t *testing.T) {
	got, err := portMappings([]*dicerdv1.PortMapping{
		{HostIp: "0.0.0.0", HostPort: 8080, GuestPort: 80},
		{HostIp: "::ffff:10.0.0.1", HostPort: 8081, GuestPort: 80},
	})
	if err != nil {
		t.Fatalf("portMappings: %v", err)
	}
	if got[0].HostIP != "" || got[1].HostIP != "10.0.0.1" {
		t.Errorf("host IPs %q and %q, want every address and 10.0.0.1", got[0].HostIP, got[1].HostIP)
	}

	// 0.0.0.0 is every address, so it overlaps one on a single address.
	_, err = portMappings([]*dicerdv1.PortMapping{
		{HostIp: "0.0.0.0", HostPort: 8080, GuestPort: 80},
		{HostIp: "10.0.0.1", HostPort: 8080, GuestPort: 81},
	})
	wantCode(t, err, codes.InvalidArgument)

	_, err = portMappings([]*dicerdv1.PortMapping{{HostIp: "::", HostPort: 8080, GuestPort: 80}})
	wantCode(t, err, codes.InvalidArgument)
}

// An instance cannot both be deleted when it stops and started again when it
// stops: one of the two would be quietly ignored.
func TestRemoveOnExitConflictsWithARestartPolicy(t *testing.T) {
	for _, mode := range []dicer.RestartMode{
		dicer.RestartAlways, dicer.RestartUnlessStopped, dicer.RestartOnFailure,
	} {
		inst := dicer.InstanceSpec{
			Name:         "web",
			RemoveOnExit: true,
			Restart:      dicer.RestartPolicy{Mode: mode},
		}

		err := checkRemoveOnExit(inst)
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("--rm with %s = %v, want an invalid argument", mode, err)
		}
	}
}

// The policies that never restart are no contradiction at all.
func TestRemoveOnExitAllowsAPolicyThatNeverRestarts(t *testing.T) {
	for _, mode := range []dicer.RestartMode{"", dicer.RestartNo} {
		inst := dicer.InstanceSpec{
			Name:         "web",
			RemoveOnExit: true,
			Restart:      dicer.RestartPolicy{Mode: mode},
		}

		if err := checkRemoveOnExit(inst); err != nil {
			t.Errorf("--rm with %q = %v, want it accepted", mode, err)
		}
	}
}

// A restart policy on its own is fine, whatever it is.
func TestARestartPolicyWithoutRemoveOnExitIsFine(t *testing.T) {
	inst := dicer.InstanceSpec{Name: "web", Restart: dicer.RestartPolicy{Mode: dicer.RestartAlways}}
	if err := checkRemoveOnExit(inst); err != nil {
		t.Errorf("a restart policy alone = %v, want it accepted", err)
	}
}
