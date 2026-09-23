// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestMounts(t *testing.T) {
	definitions, err := filestore.NewManager(filestore.Config{
		DataDir: filepath.Join(t.TempDir(), "data"),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range vm.MaxVolumeMounts + 1 {
		name := fmt.Sprintf("v%d", i)
		if err := definitions.CreateVolume(types.Volume{ID: "id-" + name, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	h := &instanceHandler{definitions: definitions}

	dir := t.TempDir()
	hostFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(hostFile, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := h.mounts([]*dicerdv1.Mount{
		{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v0", Target: "/data/"},
		{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v1", Target: "/logs", ReadOnly: true},
		{Type: dicerdv1.MountType_MOUNT_TYPE_FILE, Source: hostFile, Target: "/etc/app/secret", ReadOnly: true},
		{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Target: "/scratch"},
	})
	if err != nil {
		t.Fatalf("mounts: %v", err)
	}
	want := []types.Mount{
		{Type: types.MountVolume, Source: "v0", Target: "/data"},
		{Type: types.MountVolume, Source: "v1", Target: "/logs", ReadOnly: true},
		{Type: types.MountFile, Source: hostFile, Target: "/etc/app/secret", ReadOnly: true},
		{Type: types.MountTmpfs, Target: "/scratch"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("mounts = %+v, want %+v", got, want)
	}

	tooMany := make([]*dicerdv1.Mount, vm.MaxVolumeMounts+1)
	for i := range tooMany {
		tooMany[i] = &dicerdv1.Mount{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: fmt.Sprintf("v%d", i), Target: fmt.Sprintf("/v%d", i)}
	}

	tests := []struct {
		name string
		in   []*dicerdv1.Mount
	}{
		{"unknown type", []*dicerdv1.Mount{{Type: dicerdv1.MountType(99), Source: dir, Target: "/data"}}},
		{"unknown volume", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "nope", Target: "/data"}}},
		{"relative target", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v0", Target: "data"}}},
		{"empty target", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v0"}}},
		{"root", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v0", Target: "/."}}},
		{"same volume twice", []*dicerdv1.Mount{
			{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v0", Target: "/a"}, {Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v0", Target: "/b"},
		}},
		{"same target twice", []*dicerdv1.Mount{
			{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v0", Target: "/a"}, {Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Target: "/a/"},
		}},
		{"relative host file", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_FILE, Source: "secret", Target: "/s"}}},
		{"missing host file", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_FILE, Source: filepath.Join(dir, "nope"), Target: "/s"}}},
		{"host directory", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_FILE, Source: dir, Target: "/s"}}},
		{"tmpfs with a source", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Source: dir, Target: "/s"}}},
		{"read-only tmpfs", []*dicerdv1.Mount{{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Target: "/s", ReadOnly: true}}},
		{"too many volumes", tooMany},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.mounts(tt.in)
			wantClass(t, err, errdefs.ErrInvalidArgument)
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
		wantClass(t, validateCreate(req), errdefs.ErrInvalidArgument)
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
	wantClass(t, err, errdefs.ErrInvalidArgument)

	_, err = portMappings([]*dicerdv1.PortMapping{{HostIp: "::", HostPort: 8080, GuestPort: 80}})
	wantClass(t, err, errdefs.ErrInvalidArgument)
}

// An instance cannot both be deleted when it stops and started again when it
// stops: one of the two would be quietly ignored.
func TestRemoveOnExitConflictsWithARestartPolicy(t *testing.T) {
	for _, mode := range []types.RestartMode{
		types.RestartAlways, types.RestartUnlessStopped, types.RestartOnFailure,
	} {
		inst := types.InstanceSpec{
			Name:         "web",
			RemoveOnExit: true,
			Restart:      types.RestartPolicy{Mode: mode},
		}

		err := checkRemoveOnExit(inst)
		if !errors.Is(err, errdefs.ErrInvalidArgument) {
			t.Errorf("--rm with %s = %v, want an invalid argument", mode, err)
		}
	}
}

// The policies that never restart are no contradiction at all.
func TestRemoveOnExitAllowsAPolicyThatNeverRestarts(t *testing.T) {
	for _, mode := range []types.RestartMode{"", types.RestartNo} {
		inst := types.InstanceSpec{
			Name:         "web",
			RemoveOnExit: true,
			Restart:      types.RestartPolicy{Mode: mode},
		}

		if err := checkRemoveOnExit(inst); err != nil {
			t.Errorf("--rm with %q = %v, want it accepted", mode, err)
		}
	}
}

// A restart policy on its own is fine, whatever it is.
func TestARestartPolicyWithoutRemoveOnExitIsFine(t *testing.T) {
	inst := types.InstanceSpec{Name: "web", Restart: types.RestartPolicy{Mode: types.RestartAlways}}
	if err := checkRemoveOnExit(inst); err != nil {
		t.Errorf("a restart policy alone = %v, want it accepted", err)
	}
}
