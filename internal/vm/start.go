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
	"syscall"
	"time"

	"gvisor.dev/gvisor/pkg/cleanup"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
)

// Start boots a defined instance. It cancels any pending restart, resets the
// restart count and clears StoppedByUser.
func (m *Manager) Start(ctx context.Context, inst types.InstanceSpec) (err error) {
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
		return errdefs.InvalidState("instance %q is already %s", inst.Name, rt.State.Lower())
	}
	m.cancelRestart(inst.ID)

	if err := m.admit(inst, inst.Resources()); err != nil {
		return err
	}
	m.setStoppedByUser(ctx, inst, false)

	if err := m.boot(ctx, inst, 0); err != nil {
		m.fail(inst.ID, err)
		m.record(inst, types.ActionDied, "Failed to start instance: "+err.Error(), nil)
		return err
	}
	return nil
}

// boot starts the VMM of an instance admission has moved to Starting and
// records it running. On failure everything acquired is undone and the caller
// records the error. The caller must hold the instance lock.
func (m *Manager) boot(ctx context.Context, inst types.InstanceSpec, restarts int) error {
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

	if err := m.prepareRuntimeDir(inst.ID); err != nil {
		return err
	}
	cu.Add(func() { _ = m.clearRuntime(inst.ID) })

	if err := ensureOverlayDisk(ctx, m.overlayDiskPath(inst), inst.DiskBytes); err != nil {
		return fmt.Errorf("provision overlay disk: %w", err)
	}

	netSetup, err := m.setupNetwork(ctx, inst)
	if err != nil {
		return err
	}
	cu.Add(netSetup.cleanup)

	if err := m.writeGuestDisks(ctx, inst, starter, boot.image, boot.mounts, netSetup, guest.Status{}); err != nil {
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
		healthCheck:       types.EffectiveHealthCheck(inst.HealthCheck, boot.image.HealthCheck),
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
	m.record(inst, types.ActionStarted, message, attrs)

	m.logger.InfoContext(ctx, "started instance",
		"instance", inst.Name, "pid", vmm.PID(), "ip", netSetup.nic.IP)
	return nil
}

// setStoppedByUser records whether a user last stopped an instance. It is
// best effort. The caller must hold the instance lock.
func (m *Manager) setStoppedByUser(ctx context.Context, inst types.InstanceSpec, stopped bool) {
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
	image       *types.Image
	kernelPath  string
	kernelArgs  string
	initrdPath  string
	mounts      []guest.Mount
	volumeDisks []hypervisor.DiskConfig
}

// resolveBoot resolves the image, kernel, initrd and mounts inst boots
// from, fetching what is missing.
func (m *Manager) resolveBoot(ctx context.Context, inst types.InstanceSpec, starter hypervisor.Starter) (bootAssets, error) {
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
	if b.mounts, b.volumeDisks, err = m.resolveMounts(inst); err != nil {
		return b, err
	}
	return b, nil
}

// image returns the image an instance boots from, pulling it only if it is
// not held locally.
func (m *Manager) image(ctx context.Context, inst types.InstanceSpec) (*types.Image, error) {
	img, err := m.images.Get(inst.ImageRef)
	if errors.Is(err, errdefs.ErrNotFound) {
		img, err = m.images.Pull(ctx, inst.ImageRef, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("get image %q: %w", inst.ImageRef, err)
	}
	return img, nil
}

// statusDevice is the guest's status disk, the fourth disk.
const statusDevice = "/dev/vdd"

// MaxVolumeMounts is how many volumes an instance can mount: /dev/vde to
// /dev/vdz.
const MaxVolumeMounts = 'z' - 'e' + 1

// vmSpec is the specification inst boots with. Disk order matters: image,
// overlay, config, status, then volumes.
func (m *Manager) vmSpec(inst types.InstanceSpec, b bootAssets, nic hypervisor.NetworkInterfaceConfig) hypervisor.VirtualMachine {
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

// resolveMounts turns inst's mounts into the guest's mount table and the
// disks behind its volumes, which follow the four fixed disks in order. Host
// files are read now, so each start sees their current contents.
func (m *Manager) resolveMounts(inst types.InstanceSpec) ([]guest.Mount, []hypervisor.DiskConfig, error) {
	if len(inst.Mounts) == 0 {
		return nil, nil, nil
	}

	var (
		mounts = make([]guest.Mount, 0, len(inst.Mounts))
		disks  []hypervisor.DiskConfig
	)
	for _, mnt := range inst.Mounts {
		gm := guest.Mount{Target: mnt.Target, ReadOnly: mnt.ReadOnly}

		switch mnt.Type {
		case types.MountVolume:
			path, err := m.volumeDisk(mnt.Source)
			if err != nil {
				return nil, nil, err
			}
			gm.Volume = &guest.VolumeSource{Device: fmt.Sprintf("/dev/vd%c", 'e'+len(disks))}
			disks = append(disks, hypervisor.DiskConfig{Path: path, ReadOnly: mnt.ReadOnly})
		case types.MountFile:
			file, err := readHostFile(mnt.Source)
			if err != nil {
				return nil, nil, fmt.Errorf("mount on %s: %w", mnt.Target, err)
			}
			gm.File = file
		case types.MountTmpfs:
			gm.Tmpfs = &guest.TmpfsSource{}
		default:
			return nil, nil, fmt.Errorf("mount on %s: unknown type %q", mnt.Target, mnt.Type)
		}

		mounts = append(mounts, gm)
	}

	return mounts, disks, nil
}

// volumeDisk finds the disk of the named volume.
func (m *Manager) volumeDisk(name string) (string, error) {
	vol, err := m.definitions.GetVolume(name)
	if err != nil {
		return "", fmt.Errorf("get volume %q: %w", name, err)
	}

	path := m.volumes.Path(vol.ID)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("volume %q disk: %w", vol.Name, err)
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
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		file.UID, file.GID = int(st.Uid), int(st.Gid)
	}
	return file, nil
}

// writeGuestDisks builds the config disk and writes st to the status disk.
func (m *Manager) writeGuestDisks(
	ctx context.Context, inst types.InstanceSpec, starter hypervisor.Starter,
	img *types.Image, mounts []guest.Mount, netSetup *networkSetup, st guest.Status,
) error {
	cfg := buildInitConfig(inst, img, mounts, netSetup, guestHalt(starter))
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
	inst types.InstanceSpec, img *types.Image, mounts []guest.Mount, netSetup *networkSetup, halt guest.Halt,
) *guest.Config {
	cfg := &guest.Config{
		Hostname:     inst.Hostname,
		Entrypoint:   img.Entrypoint,
		Cmd:          img.Cmd,
		Workdir:      img.WorkingDir,
		Env:          mergeEnv(img.Env, inst.Env),
		Mode:         cmp.Or(inst.InitMode, types.ModeAuto),
		Mounts:       mounts,
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
// guest keeps its CID. CIDs 0-2 are reserved.
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
