// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package instance

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"strconv"
	"time"

	"gvisor.dev/gvisor/pkg/cleanup"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/event"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/health"
	"github.com/konradasb/dicer/internal/hostfs"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/virtiofs"
)

// start is Start, for an instance its caller has looked up, except that it
// does not wait for the guest to boot. A guest that does not boot ends as a
// failure, for its restart policy to handle.
func (m *Manager) start(ctx context.Context, instance Spec) error {
	_, err := m.startWatched(ctx, instance)
	return err
}

// startWatched is start, returning the watch of the guest's boot, or nil
// for a guest resumed rather than booted.
func (m *Manager) startWatched(ctx context.Context, instance Spec) (_ *bootWatch, err error) {
	started := time.Now()
	defer func() { m.observeOperation(operationStart, started, err) }()

	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := m.rereadDefinition(&instance); err != nil {
		return nil, err
	}
	defer m.syncWaker(ctx, instance.ID)

	status, err := m.statusOf(instance)
	if err != nil {
		return nil, err
	}
	if status.State.IsActive() {
		return nil, errdefs.InvalidState("instance %q is already %s", instance.Name, status.State.Lowercase())
	}
	m.cancelRestart(instance.ID)

	if m.onStandby(instance) {
		return nil, m.resumeStandby(ctx, instance, 0)
	}

	if err := m.admit(instance, instance.Resources()); err != nil {
		return nil, err
	}
	m.setStoppedByUser(ctx, instance, false)

	watch, err := m.boot(ctx, instance, 0)
	if err != nil {
		m.fail(instance.ID, err)
		m.record(instance, event.ActionDied, "Failed to start instance: "+err.Error(), nil)
		return nil, err
	}
	return watch, nil
}

// Start boots a defined instance, or resumes one on standby where it was. It
// cancels any pending restart, resets the restart count and clears
// StoppedByUser. It returns once the guest has booted, which is once its
// agent answers, and an ErrInvalidState error, with what its console last
// said, if it does not boot. A guest whose workload ends cleanly as it boots
// has started.
func (m *Manager) Start(ctx context.Context, nameOrID string) error {
	instance, err := m.store.Instance(nameOrID)
	if err != nil {
		return err
	}
	watch, err := m.startWatched(ctx, instance)
	if err != nil {
		return err
	}
	return watch.wait(ctx)
}

// boot starts the VMM of an instance admission has moved to Starting, records
// it running, and returns the watch of its guest's boot. On failure
// everything acquired is undone and the caller records the error. The caller
// must hold the instance lock.
func (m *Manager) boot(ctx context.Context, instance Spec, restarts int) (*bootWatch, error) {
	booting := time.Now()
	starter, err := m.resolveStarter(instance, instance.HypervisorVersion)
	if err != nil {
		return nil, err
	}
	hypervisorType := instance.EffectiveHypervisorType()
	if hypervisor.IsDeprecated(m.starters[hypervisorType], instance.HypervisorVersion) {
		m.logger.WarnContext(ctx, "booting an instance on a deprecated hypervisor version",
			"instance", instance.Name, "hypervisor", hypervisorType, "hypervisor_version", instance.HypervisorVersion)
	}
	boot, err := m.resolveBoot(ctx, instance, starter)
	if err != nil {
		return nil, err
	}

	// Booted afresh, the disk moves on from anything frozen on standby.
	if err := m.discardStandby(instance); err != nil {
		return nil, err
	}

	cu := cleanup.Make(func() {})
	defer cu.Clean()

	if err := m.prepareRuntimeDir(instance); err != nil {
		return nil, err
	}
	cu.Add(func() { _ = m.removeRuntimeDir(instance.ID) })

	if err := ensureOverlayDisk(ctx, m.overlayDiskPath(instance), instance.DiskBytes); err != nil {
		return nil, fmt.Errorf("provision overlay disk: %w", err)
	}

	setup, err := m.setupNetwork(ctx, instance)
	if err != nil {
		return nil, err
	}
	cu.Add(setup.cleanup)

	// virtiofsd listens before the VMM, which connects to it as it starts.
	filesystems, stopShares, err := m.startShares(ctx, instance, boot.mounts.shares)
	if err != nil {
		return nil, err
	}
	cu.Add(stopShares)
	boot.filesystems = filesystems

	if err := m.writeGuestDisks(ctx, instance, starter, boot.image, boot.mounts.guest, setup, guest.Status{}); err != nil {
		return nil, err
	}

	console := newBootConsole(m.serialLogPath(instance))
	spec := m.vmSpec(instance, boot, setup.nic)
	vmm, _, err := starter.StartVM(ctx, m.hypervisorSocketPath(instance.ID), spec)
	if err != nil {
		return nil, fmt.Errorf("start vm: %w", err)
	}
	cu.Add(vmm.Terminate)

	run := runRecord{
		hypervisorVersion: starter.Version(),
		vsockCID:          vsockCID(instance.ID),
		held:              instance.Resources(),
		imageDigest:       boot.image.Digest,
		restarts:          restarts,
		healthCheck:       health.EffectiveCheck(instance.HealthCheck, boot.image.HealthCheck),
	}
	status, err := m.recordRunning(instance, vmm, run)
	if err != nil {
		return nil, err
	}

	cu.Release()
	m.supervise(ctx, instance, vmm, status)
	watch := m.watchBoot(ctx, instance, vmm, status.VsockPath, console)

	verb, why := "Started", ""
	attrs := map[string]string{"ip": setup.nic.IP}
	if restarts > 0 {
		verb, why = "Restarted", " ("+restartCount(instance.Restart, restarts)+")"
		attrs["restart_count"] = strconv.Itoa(restarts)
	}
	message := fmt.Sprintf("%s instance on %s %s in %s%s: %s, %s memory, IP %s, PID %d",
		verb, instance.EffectiveHypervisorType(), starter.Version(), humanize.Duration(time.Since(booting)), why,
		humanize.Count(instance.VCPUs, "vCPU"), humanize.Bytes(instance.MemoryBytes), setup.nic.IP, vmm.PID())
	m.record(instance, event.ActionStarted, message, attrs)

	m.logger.InfoContext(ctx, "started instance",
		"instance", instance.Name, "pid", vmm.PID(), "ip", setup.nic.IP)
	return watch, nil
}

// setStoppedByUser records whether a user last stopped an instance. It is
// best effort. The caller must hold the instance lock.
func (m *Manager) setStoppedByUser(ctx context.Context, instance Spec, stopped bool) {
	current, err := m.store.Instance(instance.ID)
	if err != nil || current.StoppedByUser == stopped {
		return
	}

	current.StoppedByUser = stopped
	if err := m.store.UpdateInstance(current); err != nil {
		m.logger.WarnContext(ctx, "cannot record whether the instance was stopped by a user",
			"instance", instance.Name, "error", err)
	}
}

// bootAssets is what an instance boots from, resolved at start.
type bootAssets struct {
	image      *image.Image
	kernelPath string
	kernelArgs string
	initrdPath string
	mounts     resolvedMounts
	// filesystems are the devices that share mounts.shares, once virtiofsd
	// serves them.
	filesystems []hypervisor.FilesystemConfig
}

// resolveBoot resolves the image, kernel, initrd and mounts instance boots
// from, fetching what is missing.
func (m *Manager) resolveBoot(ctx context.Context, instance Spec, starter hypervisor.Starter) (bootAssets, error) {
	b := bootAssets{kernelArgs: instance.KernelArgs}
	if b.kernelArgs == "" {
		b.kernelArgs = starter.DefaultKernelArgs()
	}

	// The image the instance was created with, whatever its tag names now.
	// Pulled only if it is not held, so a start needs no registry.
	pinned, err := instance.PinnedImageRef()
	if err != nil {
		return b, err
	}
	if b.image, err = m.images.Ensure(ctx, pinned, image.PullPolicyMissing); err != nil {
		return b, fmt.Errorf("get image %q: %w", pinned, err)
	}

	kernel, err := m.store.Kernel(instance.KernelName)
	if err != nil {
		return b, fmt.Errorf("get kernel %q: %w", instance.KernelName, err)
	}
	if b.kernelPath, err = m.kernels.Path(kernel); err != nil {
		return b, err
	}

	if b.initrdPath, err = m.initrds.Prepare(ctx); err != nil {
		return b, fmt.Errorf("prepare initrd: %w", err)
	}
	if b.mounts, err = m.resolveMounts(instance); err != nil {
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
func (m *Manager) vmSpec(instance Spec, b bootAssets, nic hypervisor.NetworkInterfaceConfig) hypervisor.VMSpec {
	disks := append([]hypervisor.DiskConfig{
		{Path: b.image.DiskPath, ReadOnly: true},
		{Path: overlayDiskFile},
		{Path: configDiskFile, ReadOnly: true},
		{Path: statusDiskFile},
	}, b.mounts.disks...)
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
		Filesystems:       b.filesystems,
		NetworkInterfaces: []hypervisor.NetworkInterfaceConfig{nic},
		Console:           hypervisor.ConsoleConfig{Path: serialLogFile},
		Vsock: &hypervisor.VsockConfig{
			CID:        uint32(vsockCID(instance.ID)),
			SocketPath: vsockSocketFile,
		},
	}
}

// directoryShare is a host directory an instance mounts, by the tag the
// guest mounts it with.
type directoryShare struct {
	tag, target string
	source      hostfs.Directory
	readOnly    bool
}

// resolvedMounts is what an instance's mounts become at start.
type resolvedMounts struct {
	// guest is the guest's mount table.
	guest []guest.Mount
	// disks are the disks behind the volumes, which follow the four fixed
	// disks in order.
	disks []hypervisor.DiskConfig
	// shares are the host directories shared with the guest.
	shares []directoryShare
}

// resolveMounts turns instance's mounts into the guest's mount table, the
// disks behind its volumes and the host directories it shares. Each
// directory is found under the directories the daemon allows mounting,
// now, so a change to what is allowed applies from the next start.
func (m *Manager) resolveMounts(instance Spec) (resolvedMounts, error) {
	var r resolvedMounts
	if len(instance.Mounts) == 0 {
		return r, nil
	}

	r.guest = make([]guest.Mount, 0, len(instance.Mounts))
	for _, mount := range instance.Mounts {
		guestMount := guest.Mount{Target: mount.Target, ReadOnly: mount.ReadOnly}

		switch mount.Type {
		case MountTypeVolume:
			path, err := m.volumeDisk(mount.Source)
			if err != nil {
				return resolvedMounts{}, err
			}
			guestMount.Volume = &guest.VolumeSource{Device: fmt.Sprintf("/dev/vd%c", 'e'+len(r.disks))}
			r.disks = append(r.disks, hypervisor.DiskConfig{Path: path, ReadOnly: mount.ReadOnly})
		case MountTypeFile:
			guestMount.File = &guest.FileSource{Data: mount.Content, Mode: mount.FileMode()}
		case MountTypeDirectory:
			source, err := m.allowedDirectories.Resolve(mount.Source)
			if err != nil {
				return resolvedMounts{}, fmt.Errorf("mount on %s: %w", mount.Target, err)
			}
			share := directoryShare{
				tag:      fmt.Sprintf("dicerfs%d", len(r.shares)),
				source:   source,
				target:   mount.Target,
				readOnly: mount.ReadOnly,
			}
			guestMount.Directory = &guest.DirectorySource{Tag: share.tag}
			r.shares = append(r.shares, share)
		case MountTypeTmpfs:
			guestMount.Tmpfs = &guest.TmpfsSource{}
		default:
			return resolvedMounts{}, fmt.Errorf("mount on %s: unknown type %q", mount.Target, mount.Type)
		}

		r.guest = append(r.guest, guestMount)
	}

	return r, nil
}

// errNoShares is why an instance that mounts a host directory cannot start
// when dicerd could not set virtiofsd up.
var errNoShares = errdefs.InvalidState(
	"this host cannot share directories with guests: dicerd logged why when it started")

// startShares starts virtiofsd for each directory an instance shares, and
// returns the devices that share them and a function that stops them. They
// stop of their own accord when the VMM that connects to them does.
func (m *Manager) startShares(
	ctx context.Context, instance Spec, shares []directoryShare,
) ([]hypervisor.FilesystemConfig, func(), error) {
	if len(shares) == 0 {
		return nil, func() {}, nil
	}
	if m.shares == nil {
		return nil, nil, errNoShares
	}
	if hv := instance.EffectiveHypervisorType(); hv != hypervisor.TypeCloudHypervisor {
		return nil, nil, errdefs.InvalidState("directory mounts need %s, not %s",
			hypervisor.TypeCloudHypervisor, hv)
	}

	var procs []*process.Process
	stop := func() {
		for _, p := range procs {
			p.Terminate()
		}
	}

	filesystems := make([]hypervisor.FilesystemConfig, 0, len(shares))
	for i, share := range shares {
		p, err := m.shares.Start(ctx, virtiofs.Share{
			Dir:      share.source.Path,
			DirInfo:  share.source.Info,
			Socket:   m.shareSocketPath(instance.ID, i),
			ReadOnly: share.readOnly,
			Log:      m.shareLogPath(instance, i),
		})
		if err != nil {
			stop()
			return nil, nil, fmt.Errorf("mount on %s: %w", share.target, err)
		}
		procs = append(procs, p)
		// The VMM, in the runtime directory, is given the socket by name.
		filesystems = append(filesystems, hypervisor.FilesystemConfig{Tag: share.tag, Socket: shareSocketFile(i)})
	}
	return filesystems, stop, nil
}

// volumeDisk finds the disk of the named volume.
func (m *Manager) volumeDisk(name string) (string, error) {
	volume, err := m.store.Volume(name)
	if err != nil {
		return "", fmt.Errorf("get volume %q: %w", name, err)
	}

	path := m.volumes.Path(volume)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("volume %q disk: %w", volume.Name, err)
	}
	return path, nil
}

// writeGuestDisks builds the config disk and writes guestStatus to the status
// disk.
func (m *Manager) writeGuestDisks(
	ctx context.Context, instance Spec, starter hypervisor.Starter,
	image *image.Image, mounts []guest.Mount, setup *networkSetup, guestStatus guest.Status,
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
	instance Spec, image *image.Image, mounts []guest.Mount, setup *networkSetup, halt guest.Halt,
) *guest.Config {
	cfg := &guest.Config{
		Hostname:     instance.Hostname,
		Entrypoint:   image.Entrypoint,
		Cmd:          image.Cmd,
		Workdir:      image.WorkingDir,
		Env:          mergeEnv(image.Env, instance.Env),
		Mode:         cmp.Or(instance.InitMode, guest.InitModeAuto),
		User:         cmp.Or(instance.User, image.User),
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
