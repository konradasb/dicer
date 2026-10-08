// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestMountsFromProtoCleansTargets(t *testing.T) {
	got, err := mountsFromProto([]*dicerdv1.Mount{
		{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "v0", Target: "/data/"},
		{Type: dicerdv1.MountType_MOUNT_TYPE_FILE, Content: []byte("s3cret"), Mode: 0o600, Target: "/etc//app/secret", ReadOnly: true},
		{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS},
	})
	if err != nil {
		t.Fatalf("mountsFromProto: %v", err)
	}
	want := []types.Mount{
		{Type: types.MountTypeVolume, Source: "v0", Target: "/data"},
		{Type: types.MountTypeFile, Content: []byte("s3cret"), Mode: 0o600, Target: "/etc/app/secret", ReadOnly: true},
		{Type: types.MountTypeTmpfs},
	}
	if !slices.EqualFunc(got, want, types.Mount.Equal) {
		t.Errorf("mounts = %+v, want %+v", got, want)
	}

	_, err = mountsFromProto([]*dicerdv1.Mount{{Type: dicerdv1.MountType(99), Target: "/data"}})
	wantClass(t, err, errdefs.ErrInvalidArgument)
}

// TestCheckMounts checks what of an instance's mounts needs the host: that
// its volumes exist and fit.
func TestCheckMounts(t *testing.T) {
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

	if err := h.checkMounts([]types.Mount{
		{Type: types.MountTypeVolume, Source: "v0", Target: "/data"},
		{Type: types.MountTypeFile, Content: []byte("s3cret"), Target: "/etc/app/secret", ReadOnly: true},
		{Type: types.MountTypeTmpfs, Target: "/scratch"},
	}); err != nil {
		t.Errorf("checkMounts = %v, want nil", err)
	}

	tooMany := make([]types.Mount, vm.MaxVolumeMounts+1)
	for i := range tooMany {
		tooMany[i] = types.Mount{Type: types.MountTypeVolume, Source: fmt.Sprintf("v%d", i), Target: fmt.Sprintf("/v%d", i)}
	}
	tests := []struct {
		name   string
		mounts []types.Mount
	}{
		{"unknown volume", []types.Mount{{Type: types.MountTypeVolume, Source: "nope", Target: "/data"}}},
		{"too many volumes", tooMany},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantClass(t, h.checkMounts(tt.mounts), errdefs.ErrInvalidArgument)
		})
	}
}

func TestPortMappingsFromProtoCanonicalHostIP(t *testing.T) {
	got, err := portMappingsFromProto([]*dicerdv1.PortMapping{
		{HostIp: "0.0.0.0", HostPort: 8080, GuestPort: 80},
		{HostIp: "::ffff:10.0.0.1", HostPort: 8081, GuestPort: 80},
	})
	if err != nil {
		t.Fatalf("portMappingsFromProto: %v", err)
	}
	if got[0].HostIP != "" || got[1].HostIP != "10.0.0.1" {
		t.Errorf("host IPs %q and %q, want every address and 10.0.0.1", got[0].HostIP, got[1].HostIP)
	}
}

func TestPortMappingsFromProto(t *testing.T) {
	t.Run("defaults the protocol to tcp", func(t *testing.T) {
		got, err := portMappingsFromProto([]*dicerdv1.PortMapping{{HostPort: 8080, GuestPort: 80}})
		if err != nil {
			t.Fatalf("portMappingsFromProto: %v", err)
		}
		want := types.PortMapping{HostPort: 8080, GuestPort: 80, Protocol: types.ProtocolTCP}
		if len(got) != 1 || got[0] != want {
			t.Errorf("got %+v, want [%+v]", got, want)
		}
	})

	t.Run("rejects a port out of range", func(t *testing.T) {
		_, err := portMappingsFromProto([]*dicerdv1.PortMapping{{HostPort: 65536 + 80, GuestPort: 80}})
		wantClass(t, err, errdefs.ErrInvalidArgument)
	})

	t.Run("empty input yields no mappings", func(t *testing.T) {
		got, err := portMappingsFromProto(nil)
		if err != nil || got != nil {
			t.Errorf("portMappingsFromProto(nil) = %v, %v; want nil, nil", got, err)
		}
	})
}
