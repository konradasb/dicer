// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"strconv"
	"syscall"
	"time"

	"gvisor.dev/gvisor/pkg/cleanup"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// Start boots a defined instance, or resumes one on standby where it was. It
// cancels any pending restart, resets the restart count and clears
// StoppedByUser.
func (m *Manager) Start(ctx context.Context, instance types.InstanceSpec) (err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationStart, started, err) }()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := m.Status(instance)
	if err != nil {
		return err
	}
	if status.State.IsActive() {
		return errdefs.InvalidState("instance %q is already %s", instance.Name, status.State.Lowercase())
	}
	m.cancelRestart(instance.ID)

	if m.onStandby(instance) {
		return m.resumeStandby(ctx, instance)
	}

	if err := m.admit(instance, instance.Resources()); err != nil {
		return err
	}
	m.setStoppedByUser(ctx, instance, false)

	if err := m.boot(ctx, instance, 0); err != nil {
		m.fail(instance.ID, err)
		m.record(instance, events.ActionDied, "Failed to start instance: "+err.Error(), nil)
		return err
	}
	return nil
}

// boot starts the VMM of an instance admission has moved to Starting and
// records it running. On failure everything acquired is undone and the caller
// records the error. The caller must hold the instance lock.
func (m *Manager) boot(ctx context.Context, instance types.InstanceSpec, restarts int) error {
	booting := time.Now()
	starter, err := m.resolveStarter(instance, instance.HypervisorVersion)
	if err != nil {
		return err
	}
	boot, err := m.resolveBoot(ctx, instance, starter)
	if err != nil {
		return err
	}

	// Booted afresh, the disk moves on from anything frozen on standby.
	if err := m.discardStandby(instance); err != nil {
		return err
	}

	cu := cleanup.Make(func() {})
	defer cu.Clean()

	if err := m.prepareRuntimeDir(instance); err != nil {
		return err
	}
	cu.Add(func() { _ = m.removeRuntimeDir(instance.ID) })

	if err := ensureOverlayDisk(ctx, m.overlayDiskPath(instance), instance.DiskBytes); err != nil {
		return fmt.Errorf("provision overlay disk: %w", err)
	}

	setup, err := m.setupNetwork(ctx, instance)
	if err != nil {
		return err
	}
	cu.Add(setup.cleanup)

	if err := m.writeGuestDisks(ctx, instance, starter, boot.image, boot.mounts, setup, guest.Status{}); err != nil {
		return err
	}

	spec := m.vmSpec(instance, boot, setup.nic)
	vmm, _, err := starter.StartVM(ctx, m.hypervisorSocketPath(instance.ID), spec)
	if err != nil {
		return fmt.Errorf("start vm: %w", err)
	}
	cu.Add(vmm.Terminate)

	run := runRecord{
		hypervisorVersion: starter.Version(),
		vsockCID:          vsockCID(instance.ID),
		held:              instance.Resources(),
		imageDigest:       boot.image.Digest,
		restarts:          restarts,
		healthCheck:       types.EffectiveHealthCheck(instance.HealthCheck, boot.image.HealthCheck),
	}
	status, err := m.recordRunning(instance, vmm, run)
	if err != nil {
		return err
	}

	cu.Release()
	m.supervise(ctx, instance, vmm, status)

	verb, why := "Started", ""
	attrs := map[string]string{"ip": setup.nic.IP}
	if restarts > 0 {
		verb, why = "Restarted", " ("+restartCount(instance.Restart, restarts)+")"
		attrs["restart_count"] = strconv.Itoa(restarts)
	}
	message := fmt.Sprintf("%s instance on %s %s in %s%s: %s, %s memory, IP %s, PID %d",
		verb, instance.EffectiveHypervisorType(), starter.Version(), humanize.Duration(time.Since(booting)), why,
		humanize.Count(instance.VCPUs, "vCPU"), humanize.Bytes(instance.MemoryBytes), setup.nic.IP, vmm.PID())
	m.record(instance, events.ActionStarted, message, attrs)

	m.logger.InfoContext(ctx, "started instance",
		"instance", instance.Name, "pid", vmm.PID(), "ip", setup.nic.IP)
	return nil
}

// setStoppedByUser records whether a user last stopped an instance. It is
// best effort. The caller must hold the instance lock.
func (m *Manager) setStoppedByUser(ctx context.Context, instance types.InstanceSpec, stopped bool) {
	current, err := m.definitions.Instance(instance.ID)
	if err != nil || current.StoppedByUser == stopped {
		return
	}

	current.StoppedByUser = stopped
	if err := m.definitions.UpdateInstance(current); err != nil {
		m.logger.WarnContext(ctx, "cannot record whether the instance was stopped by a user",
			"instance", instance.Name, "error", err)
	}
}

// bootAssets is what an instance boots from, resolved at start.
type bootAssets struct {
	image       *types.Image
	kernelPath  string
	kernelArgs  string
	initrdPath  string
	mounts      []guest.Mount
	volumeDisks []hypervisor.DiskConfig
}

// resolveBoot resolves the image, kernel, initrd and mounts instance boots
// from, fetching what is missing.
func (m *Manager) resolveBoot(ctx context.Context, instance types.InstanceSpec, starter hypervisor.Starter) (bootAssets, error) {
	b := bootAssets{kernelArgs: instance.KernelArgs}
	if b.kernelArgs == "" {
		b.kernelArgs = starter.DefaultKernelArgs()
	}

	// Pulled only if it is not held, so a start needs no registry.
	var err error
	if b.image, err = m.images.Ensure(ctx, instance.ImageRef, types.PullPolicyMissing); err != nil {
		return b, fmt.Errorf("get image %q: %w", instance.ImageRef, err)
	}

	kernel, err := m.definitions.Kernel(instance.KernelName)
	if err != nil {
		return b, fmt.Errorf("get kernel %q: %w", instance.KernelName, err)
	}
	if b.kernelPath, err = m.kernels.Path(ctx, kernel); err != nil {
		return b, fmt.Errorf("provision kernel %q: %w", instance.KernelName, err)
	}

	if b.initrdPath, err = m.initrds.Prepare(ctx); err != nil {
		return b, fmt.Errorf("prepare initrd: %w", err)
	}
	if b.mounts, b.volumeDisks, err = m.resolveMounts(instance); err != nil {
		return b, err
	}
	return b, nil
}

// statusDevice is the guest's status disk, the fourth disk.
const statusDevice = "/dev/vdd"

// MaxVolumeMounts is how many volumes an instance can mount: /dev/vde to
// /dev/vdz.
const MaxVolumeMounts = 'z' - 'e' + 1

// vmSpec is the specification instance boots with. Disk order matters: image,
// overlay, config, status, then volumes. Each disk has the instance's disk
// rate limits. The instance's own files are named relative to its runtime
// directory, where the VMM runs; the image and volumes, which are not the
// instance's alone, by their absolute paths.
func (m *Manager) vmSpec(instance types.InstanceSpec, b bootAssets, nic hypervisor.NetworkInterfaceConfig) hypervisor.VMSpec {
	disks := append([]hypervisor.DiskConfig{
		{Path: b.image.DiskPath, ReadOnly: true},
		{Path: overlayDiskFile},
		{Path: configDiskFile, ReadOnly: true},
		{Path: statusDiskFile},
	}, b.volumeDisks...)
	for i := range disks {
		disks[i].RateLimitBytesPerSecond = instance.DiskBytesPerSecond
		disks[i].RateLimitIOPS = instance.DiskIOPS
	}

	return hypervisor.VMSpec{
		Boot: hypervisor.BootConfig{
			KernelPath: b.kernelPath,
			KernelArgs: b.kernelArgs,
			InitrdPath: b.initrdPath,
		},
		CPU: hypervisor.CPUConfig{Count: instance.VCPUs, MaxCount: instance.MaxVCPUs},
		Memory: hypervisor.MemoryConfig{
			SizeBytes:    instance.MemoryBytes,
			HotplugBytes: max(instance.MaxMemoryBytes-instance.MemoryBytes, 0),
		},
		Disks:             disks,
		NetworkInterfaces: []hypervisor.NetworkInterfaceConfig{nic},
		Console:           hypervisor.ConsoleConfig{Path: serialLogFile},
		Vsock: &hypervisor.VsockConfig{
			CID:        uint32(vsockCID(instance.ID)),
			SocketPath: vsockSocketFile,
		},
	}
}

// resolveMounts turns instance's mounts into the guest's mount table and the
// disks behind its volumes, which follow the four fixed disks in order. Host
// files are read now, so each start sees their current contents.
func (m *Manager) resolveMounts(instance types.InstanceSpec) ([]guest.Mount, []hypervisor.DiskConfig, error) {
	if len(instance.Mounts) == 0 {
		return nil, nil, nil
	}

	var (
		mounts = make([]guest.Mount, 0, len(instance.Mounts))
		disks  []hypervisor.DiskConfig
	)
	for _, mount := range instance.Mounts {
		guestMount := guest.Mount{Target: mount.Target, ReadOnly: mount.ReadOnly}

		switch mount.Type {
		case types.MountTypeVolume:
			path, err := m.volumeDisk(mount.Source)
			if err != nil {
				return nil, nil, err
			}
			guestMount.Volume = &guest.VolumeSource{Device: fmt.Sprintf("/dev/vd%c", 'e'+len(disks))}
			disks = append(disks, hypervisor.DiskConfig{Path: path, ReadOnly: mount.ReadOnly})
		case types.MountTypeFile:
			file, err := readHostFile(mount.Source)
			if err != nil {
				return nil, nil, fmt.Errorf("mount on %s: %w", mount.Target, err)
			}
			guestMount.File = file
		case types.MountTypeTmpfs:
			guestMount.Tmpfs = &guest.TmpfsSource{}
		default:
			return nil, nil, fmt.Errorf("mount on %s: unknown type %q", mount.Target, mount.Type)
		}

		mounts = append(mounts, guestMount)
	}

	return mounts, disks, nil
}

// volumeDisk finds the disk of the named volume.
func (m *Manager) volumeDisk(name string) (string, error) {
	volume, err := m.definitions.Volume(name)
	if err != nil {
		return "", fmt.Errorf("get volume %q: %w", name, err)
	}

	path := m.volumes.Path(volume.ID)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("volume %q disk: %w", volume.Name, err)
	}
	return path, nil
}

// readHostFile reads a host file with the permissions and owner the guest's
// copy gets.
func readHostFile(path string) (*guest.FileSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read host file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read host file: %w", err)
	}

	file := &guest.FileSource{Data: data, Mode: uint32(info.Mode().Perm())}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		file.UID, file.GID = int(stat.Uid), int(stat.Gid)
	}
	return file, nil
}

// writeGuestDisks builds the config disk and writes guestStatus to the status
// disk.
func (m *Manager) writeGuestDisks(
	ctx context.Context, instance types.InstanceSpec, starter hypervisor.Starter,
	image *types.Image, mounts []guest.Mount, setup *networkSetup, guestStatus guest.Status,
) error {
	cfg := buildInitConfig(instance, image, mounts, setup, guestHalt(starter))
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid init config: %w", err)
	}

	if err := m.provisionConfigDisk(ctx, m.configDiskPath(instance.ID), cfg); err != nil {
		return fmt.Errorf("provision config disk: %w", err)
	}

	return writeStatusDisk(m.statusDiskPath(instance.ID), guestStatus)
}

// guestHalt is how a guest on starter's hypervisor ends its VMM.
func guestHalt(starter hypervisor.Starter) guest.Halt {
	if starter.PowerOffEndsVM() {
		return guest.HaltPowerOff
	}
	return guest.HaltReset
}

// buildInitConfig assembles the configuration handed to dicer-init in the
// guest.
func buildInitConfig(
	instance types.InstanceSpec, image *types.Image, mounts []guest.Mount, setup *networkSetup, halt guest.Halt,
) *guest.Config {
	cfg := &guest.Config{
		Hostname:     instance.Hostname,
		Entrypoint:   image.Entrypoint,
		Cmd:          image.Cmd,
		Workdir:      image.WorkingDir,
		Env:          mergeEnv(image.Env, instance.Env),
		Mode:         cmp.Or(instance.InitMode, types.InitModeAuto),
		Mounts:       mounts,
		StatusDevice: statusDevice,
		Halt:         halt,
	}
	if instance.Hostname == "" {
		cfg.Hostname = instance.Name
	}

	cfg.Network = guest.NetworkConfig{
		DNS: guest.DNSConfig{Nameservers: setup.nameservers},
		Interfaces: []guest.NetworkInterface{{
			Name:      guestInterface,
			Addresses: []string{setup.guestAddress()},
			MTU:       setup.nic.MTU,
		}},
		Routes: []guest.NetworkRoute{{Destination: "default", Gateway: setup.gateway}},
	}

	if len(instance.Cmd) > 0 {
		cfg.Entrypoint = instance.Cmd
		cfg.Cmd = nil
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

// vsockCID derives a stable context ID from the instance ID, so a restored
// guest keeps its CID, as a fork keeps its source's. CIDs 0-2 are reserved.
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
