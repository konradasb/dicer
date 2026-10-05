// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
)

// TestResolveMountsAttachesExistingDisk guards the volume's whole purpose:
// its data must outlive a restart, so every start attaches the disk the
// volume was created with rather than making a new one.
func TestResolveMountsAttachesExistingDisk(t *testing.T) {
	manager, definitions, _ := newTestManager(t)
	volumes := fakeVolumes{dir: t.TempDir()}
	manager.volumes = volumes

	definitions.volumes["data"] = types.Volume{ID: "vol-1", Name: "data"}
	disk := volumes.Path("vol-1")
	if err := os.MkdirAll(filepath.Dir(disk), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("existing data"), 0o600); err != nil {
		t.Fatal(err)
	}

	instance := seedInstance(t, definitions, "web")
	instance.Mounts = []types.Mount{{Type: types.MountTypeVolume, Source: "data", Target: "/data"}}

	mounts, disks, err := manager.resolveMounts(instance)
	if err != nil {
		t.Fatalf("resolveMounts: %v", err)
	}
	if len(disks) != 1 || disks[0].Path != disk || disks[0].ReadOnly {
		t.Errorf("disks = %+v, want the existing read-write disk at %s", disks, disk)
	}
	if len(mounts) != 1 || mounts[0].Volume == nil || mounts[0].Volume.Device != "/dev/vde" {
		t.Errorf("mounts = %+v, want the disk mounted from /dev/vde", mounts)
	}
}

func TestResolveMountsMissingDisk(t *testing.T) {
	manager, definitions, _ := newTestManager(t)
	manager.volumes = fakeVolumes{dir: t.TempDir()}

	definitions.volumes["data"] = types.Volume{ID: "vol-1", Name: "data"}
	instance := seedInstance(t, definitions, "web")
	instance.Mounts = []types.Mount{{Type: types.MountTypeVolume, Source: "data", Target: "/data"}}

	if _, _, err := manager.resolveMounts(instance); err == nil {
		t.Error("resolveMounts succeeded for a volume whose disk is gone")
	}
}

// TestResolveMountsMixed checks each type becomes its guest mount, that
// volume disks are lettered in order past the other mounts, and that a host
// file is read at start with its permissions.
func TestResolveMountsMixed(t *testing.T) {
	manager, definitions, _ := newTestManager(t)
	volumes := fakeVolumes{dir: t.TempDir()}
	manager.volumes = volumes

	for _, name := range []string{"a", "b"} {
		definitions.volumes[name] = types.Volume{ID: "vol-" + name, Name: name}
		disk := volumes.Path("vol-" + name)
		if err := os.MkdirAll(filepath.Dir(disk), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(disk, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hostFile := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(hostFile, []byte("k=v"), 0o640); err != nil {
		t.Fatal(err)
	}

	instance := seedInstance(t, definitions, "web")
	instance.Mounts = []types.Mount{
		{Type: types.MountTypeVolume, Source: "a", Target: "/a"},
		{Type: types.MountTypeFile, Source: hostFile, Target: "/etc/app.conf", ReadOnly: true},
		{Type: types.MountTypeTmpfs, Target: "/scratch"},
		{Type: types.MountTypeVolume, Source: "b", Target: "/b", ReadOnly: true},
	}

	mounts, disks, err := manager.resolveMounts(instance)
	if err != nil {
		t.Fatalf("resolveMounts: %v", err)
	}

	if len(disks) != 2 || disks[0].ReadOnly || !disks[1].ReadOnly {
		t.Errorf("disks = %+v, want a read-write and then a read-only disk", disks)
	}
	if len(mounts) != 4 {
		t.Fatalf("mounts = %+v, want four", mounts)
	}
	if v := mounts[0].Volume; v == nil || v.Device != "/dev/vde" {
		t.Errorf("mounts[0] = %+v, want /dev/vde", mounts[0])
	}
	if f := mounts[1].File; f == nil || string(f.Data) != "k=v" || f.Mode != 0o640 || !mounts[1].ReadOnly {
		t.Errorf("mounts[1] = %+v, want the host file's contents, mode 0640, read-only", mounts[1])
	}
	if mounts[2].Tmpfs == nil {
		t.Errorf("mounts[2] = %+v, want a tmpfs", mounts[2])
	}
	if v := mounts[3].Volume; v == nil || v.Device != "/dev/vdf" || !mounts[3].ReadOnly {
		t.Errorf("mounts[3] = %+v, want /dev/vdf, read-only", mounts[3])
	}
}

// TestStartOfDeletedInstanceIsRefused covers a start that looked the
// instance up, then waited for its lock while the instance was deleted: it
// must not boot a VM for an instance that no longer exists.
func TestStartOfDeletedInstanceIsRefused(t *testing.T) {
	h := newHarness(t)
	if err := h.manager.Delete(t.Context(), h.instance, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := h.manager.Start(t.Context(), h.instance); !errors.Is(err, errdefs.ErrNotFound) {
		t.Fatalf("Start = %v, want a refusal for an instance that no longer exists", err)
	}
	if n := h.starter.vmmCount(); n != 0 {
		t.Errorf("%d VMMs launched for a deleted instance", n)
	}
}

// TestStartUsesTheImageHeld checks that a start boots the image the host
// already holds without asking a registry, which may be unreachable or
// rate-limiting the host.
func TestStartUsesTheImageHeld(t *testing.T) {
	h := newHarness(t)
	images, ok := h.manager.images.(*fakeImages)
	if !ok {
		t.Fatalf("images is a %T, want the fake", h.manager.images)
	}
	images.held = &types.Image{
		Name: h.instance.ImageRef, Digest: "sha256:bbbb", DiskPath: images.diskPath, Entrypoint: []string{"/bin/sh"},
	}

	h.start(t)

	if images.pulls != 0 {
		t.Errorf("pulls = %d, want none for an image the host holds", images.pulls)
	}
}

// TestStartAppliesRateLimits checks that an instance's disk limits reach
// every one of its disks and its bandwidth limits its TAP device.
func TestStartAppliesRateLimits(t *testing.T) {
	h := newHarness(t)
	h.instance.DiskBytesPerSecond = 50 << 20
	h.instance.DiskIOPS = 1000
	h.instance.UploadBytesPerSecond = 1 << 20
	h.instance.DownloadBytesPerSecond = 2 << 20
	h.definitions.instances[h.instance.Name] = h.instance

	h.start(t)

	for _, disk := range h.starter.spec.Disks {
		if disk.RateLimitBytesPerSecond != 50<<20 || disk.RateLimitIOPS != 1000 {
			t.Errorf("disk %s limited to %d bytes and %d operations a second, want 50 MiB and 1000",
				disk.Path, disk.RateLimitBytesPerSecond, disk.RateLimitIOPS)
		}
	}
	want := network.Bandwidth{UploadBytesPerSecond: 1 << 20, DownloadBytesPerSecond: 2 << 20}
	if h.hostNetwork.bandwidth != want {
		t.Errorf("TAP device limited to %+v, want %+v", h.hostNetwork.bandwidth, want)
	}
}

// The guest decides how to start the command, unless the instance says: the
// host no longer guesses from the image's entrypoint.
func TestInitConfigCarriesTheInitMode(t *testing.T) {
	image := &types.Image{Entrypoint: []string{"/sbin/init"}}
	setup := &networkSetup{nic: hypervisor.NetworkInterfaceConfig{IP: "10.0.0.2"}, prefixLen: 24}

	for mode, want := range map[types.InitMode]types.InitMode{
		"":                    types.InitModeAuto,
		types.InitModeExec:    types.InitModeExec,
		types.InitModeSystemd: types.InitModeSystemd,
	} {
		cfg := buildInitConfig(types.InstanceSpec{Name: "web", InitMode: mode}, image, nil, setup, guest.HaltPowerOff)
		if cfg.Mode != want {
			t.Errorf("instance mode %q: config mode = %q, want %q", mode, cfg.Mode, want)
		}
	}
}
