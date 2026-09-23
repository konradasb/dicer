// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/guest"
	"github.com/dicer-sh/dicer/internal/hypervisor"
)

// TestResolveVolumesAttachesExistingDisk guards the volume's whole purpose:
// its data must outlive a restart, so every start attaches the disk the
// volume was created with rather than making a new one.
func TestResolveVolumesAttachesExistingDisk(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	vols := fakeVolumes{dir: t.TempDir()}
	mgr.volumes = vols

	definitions.volumes["data"] = dicer.Volume{ID: "vol-1", Name: "data"}
	disk := vols.Path("vol-1")
	if err := os.MkdirAll(filepath.Dir(disk), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("existing data"), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := seedInstance(t, definitions, "web")
	inst.VolumeMounts = []dicer.VolumeMount{{
		VolumeName: "data", MountPath: "/data", AccessMode: dicer.AccessModeReadWriteOnce,
	}}

	disks, err := mgr.resolveVolumes(inst)
	if err != nil {
		t.Fatalf("resolveVolumes: %v", err)
	}
	if len(disks) != 1 || disks[0].Path != disk || disks[0].ReadOnly {
		t.Errorf("disks = %+v, want the existing read-write disk at %s", disks, disk)
	}
}

func TestResolveVolumesMissingDisk(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	mgr.volumes = fakeVolumes{dir: t.TempDir()}

	definitions.volumes["data"] = dicer.Volume{ID: "vol-1", Name: "data"}
	inst := seedInstance(t, definitions, "web")
	inst.VolumeMounts = []dicer.VolumeMount{{VolumeName: "data", MountPath: "/data"}}

	if _, err := mgr.resolveVolumes(inst); err == nil {
		t.Error("resolveVolumes succeeded for a volume whose disk is gone")
	}
}

// TestStartOfDeletedInstanceIsRefused covers a start that looked the
// instance up, then waited for its lock while the instance was deleted: it
// must not boot a VM for an instance that no longer exists.
func TestStartOfDeletedInstanceIsRefused(t *testing.T) {
	h := newHarness(t)
	if err := h.mgr.Delete(t.Context(), h.inst, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := h.mgr.Start(t.Context(), h.inst); !errors.Is(err, dicer.ErrNotFound) {
		t.Fatalf("Start = %v, want a refusal for an instance that no longer exists", err)
	}
	if n := len(h.starter.vmms); n != 0 {
		t.Errorf("%d VMMs launched for a deleted instance", n)
	}
}

// TestStartUsesTheImageHeld checks that a start boots the image the host
// already holds without asking a registry, which may be unreachable or
// rate-limiting the host.
func TestStartUsesTheImageHeld(t *testing.T) {
	h := newHarness(t)
	images, ok := h.mgr.images.(*fakeImages)
	if !ok {
		t.Fatalf("images is a %T, want the fake", h.mgr.images)
	}
	images.held = &dicer.Image{
		Name: h.inst.ImageRef, Digest: "sha256:bbbb", DiskPath: images.diskPath, Entrypoint: []string{"/bin/sh"},
	}

	h.start(t)

	if images.pulls != 0 {
		t.Errorf("pulls = %d, want none for an image the host holds", images.pulls)
	}
}

// The guest decides how to start the command, unless the instance says: the
// host no longer guesses from the image's entrypoint.
func TestInitConfigCarriesTheInitMode(t *testing.T) {
	img := &dicer.Image{Entrypoint: []string{"/sbin/init"}}
	net := &networkSetup{nic: hypervisor.NetworkInterfaceConfig{IP: "10.0.0.2"}, prefixLen: 24}

	for mode, want := range map[dicer.InitMode]dicer.InitMode{
		"":                dicer.ModeAuto,
		dicer.ModeExec:    dicer.ModeExec,
		dicer.ModeSystemd: dicer.ModeSystemd,
	} {
		cfg := buildInitConfig(dicer.InstanceSpec{Name: "web", InitMode: mode}, img, nil, net, guest.HaltPowerOff)
		if cfg.Mode != want {
			t.Errorf("instance mode %q: config mode = %q, want %q", mode, cfg.Mode, want)
		}
	}
}
