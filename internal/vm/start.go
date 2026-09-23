// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"time"

	"gvisor.dev/gvisor/pkg/cleanup"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/guest"
	"github.com/dicer-sh/dicer/internal/hypervisor"
)

// Start boots a defined instance.
//
// Referenced resources are resolved here rather than at definition time, so
// an instance picks up an edited network or a rotated credential file on its
// next start instead of carrying a frozen copy.
//
// A start is the user taking the instance over: a restart it was waiting on
// is cancelled, its count of restarts in a row starts again from zero, and it
// is no longer one a user stopped.
func (m *Manager) Start(ctx context.Context, inst dicer.InstanceSpec) (err error) {
	started := time.Now()
	defer func() { m.observe(opStart, started, err) }()

	lock := m.lock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	rt, err := m.Runtime(inst)
	if err != nil {
		return err
	}
	if rt.State.IsActive() {
		return dicer.InvalidState("instance %q is already %s", inst.Name, rt.State.Lower())
	}
	m.cancelRestart(inst.ID)

	if err := m.admit(inst, inst.Resources()); err != nil {
		return err
	}
	m.setStoppedByUser(ctx, inst, false)

	// Any error from here on leaves the instance Failed with the reason
	// recorded, rather than stuck in Starting.
	if err := m.boot(ctx, inst, 0); err != nil {
		m.fail(inst.ID, err)
		m.record(inst, dicer.ActionDied, "Failed to start instance: "+err.Error(), nil)
		return err
	}
	return nil
}

// boot starts the VMM of an instance admission has moved to Starting, and
// records it running with restarts restarts in a row behind it. A start and
// a restart differ only in how they get here.
//
// Every step that acquires something registers its undo with the cleanup
// stack, so a failure at any point leaves the host as it was found; the
// caller records the failure. The caller must hold the instance lock.
func (m *Manager) boot(ctx context.Context, inst dicer.InstanceSpec, restarts int) error {
	booting := time.Now()
	starter, err := m.resolveStarter(inst, inst.HypervisorVersion)
	if err != nil {
		return err
	}
	boot, err := m.resolveBoot(ctx, inst, starter)
	if err != nil {
		return err
	}

	cu := cleanup.Make(func() {})
	defer cu.Clean()

	// The runtime directory holds sockets and the config disk, and is
	// cleared on failure. The persistent instance directory is left alone:
	// it holds the definition and the overlay disk.
	if err := m.prepareRuntimeDir(inst.ID); err != nil {
		return err
	}
	cu.Add(func() { _ = m.clearRuntime(inst.ID) })

	// The overlay disk is the instance's root filesystem and must survive a
	// stop/start cycle, so it lives next to the definition and is only
	// created if absent.
	if err := ensureOverlayDisk(ctx, m.overlayDiskPath(inst), inst.DiskBytes); err != nil {
		return fmt.Errorf("provision overlay disk: %w", err)
	}

	netSetup, err := m.setupNetwork(ctx, inst)
	if err != nil {
		return err
	}
	cu.Add(netSetup.cleanup)

	if err := m.writeGuestDisks(ctx, inst, starter, boot.image, boot.files, netSetup, guest.Status{}); err != nil {
		return err
	}

	spec := m.vmSpec(inst, boot, netSetup.nic)
	vmm, _, err := starter.StartVM(ctx, m.hypervisorSocketPath(inst.ID), spec)
	if err != nil {
		return fmt.Errorf("start vm: %w", err)
	}
	cu.Add(vmm.Terminate)

	run := runRecord{
		hypervisorVersion: starter.Version(),
		held:              inst.Resources(),
		imageDigest:       boot.image.Digest,
		restarts:          restarts,
		healthCheck:       dicer.EffectiveHealthCheck(inst.HealthCheck, boot.image.HealthCheck),
	}
	rt, err := m.recordRunning(inst, vmm, run)
	if err != nil {
		return err
	}

	cu.Release()
	m.supervise(ctx, inst, vmm, rt)

	verb, why := "Started", ""
	attrs := map[string]string{"ip": netSetup.nic.IP}
	if restarts > 0 {
		verb, why = "Restarted", " ("+restartCount(inst.Restart, restarts)+")"
		attrs["restart_count"] = strconv.Itoa(restarts)
	}
	message := fmt.Sprintf("%s instance on %s %s in %s%s: %s, %s memory, IP %s, PID %d",
		verb, inst.Hypervisor(), starter.Version(), duration(time.Since(booting)), why,
		vcpus(inst.VCPUs), size(inst.MemoryBytes), netSetup.nic.IP, vmm.PID())
	m.record(inst, dicer.ActionStarted, message, attrs)

	m.logger.InfoContext(ctx, "started instance",
		"instance", inst.Name, "pid", vmm.PID(), "ip", netSetup.nic.IP)
	return nil
}

// setStoppedByUser records whether a user last stopped an instance, rather
// than started it. It is best effort: failing to record it only matters to
// an unless-stopped instance at the next boot, and is no reason to fail the
// start or stop that changed it. The caller must hold the instance lock.
func (m *Manager) setStoppedByUser(ctx context.Context, inst dicer.InstanceSpec, stopped bool) {
	// The caller's copy may be older than the stored definition, which is
	// the one to change.
	current, err := m.definitions.GetInstance(inst.ID)
	if err != nil || current.StoppedByUser == stopped {
		return
	}

	current.StoppedByUser = stopped
	if err := m.definitions.UpdateInstance(current); err != nil {
		m.logger.WarnContext(ctx, "cannot record whether the instance was stopped by a user",
			"instance", inst.Name, "error", err)
	}
}

// bootAssets is what an instance boots from, resolved at start.
type bootAssets struct {
	image       *dicer.Image
	kernelPath  string
	kernelArgs  string
	initrdPath  string
	files       []guest.FileMount
	volumeDisks []hypervisor.DiskConfig
}

// resolveBoot resolves everything inst boots from: its image, pulled if not
// cached; its kernel, fetched if not present; the initrd carrying dicer-init
// and dicer-agent; the host files to inject; and its volumes' disks.
func (m *Manager) resolveBoot(ctx context.Context, inst dicer.InstanceSpec, starter hypervisor.Starter) (bootAssets, error) {
	b := bootAssets{kernelArgs: inst.KernelArgs}
	if b.kernelArgs == "" {
		b.kernelArgs = starter.DefaultBootArgs()
	}

	var err error
	if b.image, err = m.image(ctx, inst); err != nil {
		return b, err
	}

	kern, err := m.definitions.GetKernel(inst.KernelName)
	if err != nil {
		return b, fmt.Errorf("get kernel %q: %w", inst.KernelName, err)
	}
	if b.kernelPath, err = m.kernels.Path(ctx, kern); err != nil {
		return b, fmt.Errorf("provision kernel %q: %w", inst.KernelName, err)
	}

	if b.initrdPath, err = m.initrds.Prepare(ctx); err != nil {
		return b, fmt.Errorf("prepare initrd: %w", err)
	}
	if b.files, err = m.resolveFiles(inst); err != nil {
		return b, err
	}
	if b.volumeDisks, err = m.resolveVolumes(inst); err != nil {
		return b, err
	}
	return b, nil
}

// image returns the image an instance boots from: the one this host holds
// under its reference, or else a fresh pull. A start never asks a registry
// about an image it already has -- so an offline host, or a rate-limited
// one, still starts what it has, and a tag that moved upstream does not
// change what an existing instance boots until it is pulled again.
func (m *Manager) image(ctx context.Context, inst dicer.InstanceSpec) (*dicer.Image, error) {
	img, err := m.images.Get(inst.ImageRef)
	if errors.Is(err, dicer.ErrNotFound) {
		img, err = m.images.Pull(ctx, inst.ImageRef, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("get image %q: %w", inst.ImageRef, err)
	}
	return img, nil
}

// statusDevice is where the guest finds the status disk: fourth, after the
// image, the overlay and the config disk.
const statusDevice = "/dev/vdd"

// MaxVolumeMounts is how many volumes an instance can mount. Volumes follow
// the four disks every guest has, as /dev/vde to /dev/vdz.
const MaxVolumeMounts = 'z' - 'e' + 1

// vmSpec is the specification inst boots with. The disks are attached in the
// order buildInitConfig expects them: the image, the overlay, the config
// disk, the status disk, then volumes.
func (m *Manager) vmSpec(inst dicer.InstanceSpec, b bootAssets, nic hypervisor.NetworkInterfaceConfig) hypervisor.VirtualMachine {
	return hypervisor.VirtualMachine{
		Boot: hypervisor.BootConfig{
			KernelPath: b.kernelPath,
			KernelArgs: b.kernelArgs,
			InitrdPath: b.initrdPath,
		},
		CPU:    hypervisor.CPUConfig{Count: inst.VCPUs},
		Memory: hypervisor.MemoryConfig{SizeBytes: inst.MemoryBytes},
		Disks: append([]hypervisor.DiskConfig{
			{Path: b.image.DiskPath, ReadOnly: true},
			{Path: m.overlayDiskPath(inst)},
			{Path: m.configDiskPath(inst.ID), ReadOnly: true},
			{Path: m.statusDiskPath(inst.ID)},
		}, b.volumeDisks...),
		NICs:    []hypervisor.NetworkInterfaceConfig{nic},
		Console: hypervisor.ConsoleConfig{Path: m.serialLogPath(inst)},
		Vsock: &hypervisor.VsockConfig{
			CID:    uint32(vsockCID(inst.ID)),
			Socket: m.vsockPath(inst.ID),
		},
	}
}

// resolveFiles reads the host files to be injected into the guest.
//
// They are read at start, not at definition time, so an instance picks up a
// rotated credential on its next start without being redefined. The daemon
// keeps no copy: the bytes go onto the config disk and are forgotten.
func (m *Manager) resolveFiles(inst dicer.InstanceSpec) ([]guest.FileMount, error) {
	if len(inst.Files) == 0 {
		return nil, nil
	}

	mounts := make([]guest.FileMount, 0, len(inst.Files))
	for _, f := range inst.Files {
		value, err := os.ReadFile(f.HostPath)
		if err != nil {
			return nil, fmt.Errorf("read file %q for %q: %w", f.HostPath, f.Name, err)
		}

		mounts = append(mounts, guest.FileMount{Name: f.Name, Value: value})
	}

	return mounts, nil
}

// resolveVolumes finds each mounted volume's disk. The disk was created with
// the volume and carries its data, so it is attached as it is -- never
// provisioned afresh.
func (m *Manager) resolveVolumes(inst dicer.InstanceSpec) ([]hypervisor.DiskConfig, error) {
	if len(inst.VolumeMounts) == 0 {
		return nil, nil
	}

	disks := make([]hypervisor.DiskConfig, 0, len(inst.VolumeMounts))
	for _, mount := range inst.VolumeMounts {
		vol, err := m.definitions.GetVolume(mount.VolumeName)
		if err != nil {
			return nil, fmt.Errorf("get volume %q: %w", mount.VolumeName, err)
		}

		path := m.volumes.Path(vol.ID)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("volume %q disk: %w", vol.Name, err)
		}

		disks = append(disks, hypervisor.DiskConfig{
			Path:     path,
			ReadOnly: mount.AccessMode == dicer.AccessModeReadOnlyMany,
		})
	}

	return disks, nil
}

// checkVolumes refuses an instance that would attach a volume another
// instance holds in a way the two cannot share: a ReadWriteOnce attachment
// excludes every other, and a ReadOnlyMany one excludes only ReadWriteOnce.
// Attachment is derived from the instances holding resources rather than
// recorded on the volume, so an instance that died without cleaning up does
// not hold a volume hostage.
//
// The caller must hold admissionMu, so that two starts cannot both pass.
func (m *Manager) checkVolumes(inst dicer.InstanceSpec) error {
	if len(inst.VolumeMounts) == 0 {
		return nil
	}

	instances, err := m.definitions.ListInstances()
	if err != nil {
		return err
	}

	for _, other := range instances {
		if other.ID == inst.ID || len(other.VolumeMounts) == 0 {
			continue
		}

		rt, err := m.Runtime(other)
		if err != nil {
			return err
		}
		if !rt.State.HoldsResources() && rt.State != dicer.StateStopping {
			continue
		}

		for _, mine := range inst.VolumeMounts {
			for _, theirs := range other.VolumeMounts {
				if mine.VolumeName != theirs.VolumeName {
					continue
				}
				if mine.AccessMode == dicer.AccessModeReadWriteOnce ||
					theirs.AccessMode == dicer.AccessModeReadWriteOnce {
					return dicer.InvalidState("volume %q is attached to instance %q, which is %s, "+
						"and a ReadWriteOnce volume can be attached to only one instance at a time",
						mine.VolumeName, other.Name, rt.State.Lower())
				}
			}
		}
	}

	return nil
}

// writeGuestDisks builds the disks dicer-init reads at boot: the config
// disk, and the status disk it reports the guest's end on, which starts out
// holding st.
func (m *Manager) writeGuestDisks(
	ctx context.Context, inst dicer.InstanceSpec, starter hypervisor.Starter,
	img *dicer.Image, files []guest.FileMount, netSetup *networkSetup, st guest.Status,
) error {
	cfg := buildInitConfig(inst, img, files, netSetup, guestHalt(starter))
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid init config: %w", err)
	}

	if err := m.provisionConfigDisk(ctx, m.configDiskPath(inst.ID), cfg); err != nil {
		return fmt.Errorf("provision config disk: %w", err)
	}

	return writeStatusDisk(m.statusDiskPath(inst.ID), st)
}

// guestHalt is how a guest on starter's hypervisor ends its VMM.
func guestHalt(starter hypervisor.Starter) guest.Halt {
	if starter.PowerOffEndsVM() {
		return guest.HaltPowerOff
	}
	return guest.HaltReset
}

// buildInitConfig assembles the configuration handed to dicer-init in the guest.
func buildInitConfig(
	inst dicer.InstanceSpec, img *dicer.Image, files []guest.FileMount, netSetup *networkSetup, halt guest.Halt,
) *guest.Config {
	cfg := &guest.Config{
		Hostname:     inst.Hostname,
		Entrypoint:   img.Entrypoint,
		Cmd:          img.Cmd,
		Workdir:      img.WorkingDir,
		Env:          mergeEnv(img.Env, inst.Env),
		Mode:         cmp.Or(inst.InitMode, dicer.ModeAuto),
		Files:        files,
		StatusDevice: statusDevice,
		Halt:         halt,
	}
	if inst.Hostname == "" {
		cfg.Hostname = inst.Name
	}

	cfg.Network = guest.NetworkConfig{
		DNS: guest.DNSConfig{Nameservers: netSetup.nameservers},
		Interfaces: []guest.NetworkInterface{{
			Interface: "eth0",
			Addresses: []string{fmt.Sprintf("%s/%d", netSetup.nic.IP, netSetup.prefixLen)},
			MTU:       netSetup.nic.MTU,
		}},
		Routes: []guest.NetworkRoute{{Destination: "default", Gateway: netSetup.gateway}},
	}

	if len(inst.Cmd) > 0 {
		cfg.Entrypoint = inst.Cmd
		cfg.Cmd = nil
	}

	// Volumes are attached after the rootfs, overlay, config and status
	// disks, so they start at vde.
	for i, mount := range inst.VolumeMounts {
		mode := guest.MountModeRW
		if mount.AccessMode == dicer.AccessModeReadOnlyMany {
			mode = guest.MountModeRO
		}
		cfg.VolumeMounts = append(cfg.VolumeMounts, guest.VolumeMount{
			Device: fmt.Sprintf("/dev/vd%c", 'e'+i),
			Path:   mount.MountPath,
			Mode:   mode,
		})
	}

	return cfg
}

// mergeEnv returns base with override applied on top of it.
func mergeEnv(base, override map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(override))
	maps.Copy(merged, base)
	maps.Copy(merged, override)
	return merged
}

// vsockCID derives a stable context ID from the instance ID, so that a
// restored guest finds the one it was snapshotted with. CIDs 0-2 are
// reserved by the vsock protocol, and the result stays within 32 bits.
func vsockCID(instanceID string) int64 {
	prefix := instanceID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}

	var sum int64
	for _, c := range prefix {
		sum = sum*37 + int64(c)
	}

	return (sum % 4294967292) + 3
}
