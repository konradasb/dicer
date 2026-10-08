// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// Instances are the calls about instances, reached as Client.Instances. A
// call that takes an instance's name takes its ID as well.
type Instances struct {
	api dicerdv1.DaemonServiceClient
}

// Instance is a virtual machine: its definition, and what it is doing now.
// The fields after UpdateTime are empty while it is not running.
type Instance struct {
	ID string `json:"id,omitzero"`

	InstanceSpec

	CreateTime time.Time `json:"create_time,omitzero"`
	UpdateTime time.Time `json:"update_time,omitzero"`

	State InstanceState `json:"state,omitzero"`

	// StateError says why the last operation failed, for a Failed instance.
	StateError string `json:"state_error,omitzero"`

	HypervisorPID int       `json:"hypervisor_pid,omitzero"`
	VsockCID      int64     `json:"vsock_cid,omitzero"`
	IP            string    `json:"ip,omitzero"`
	MAC           string    `json:"mac,omitzero"`
	StartTime     time.Time `json:"start_time,omitzero"`

	// ExitCode is the exit code the guest reported when it last ended on its
	// own: its workload's, or 0 for a guest that powered itself off. It is
	// nil if the instance has not ended since it was last started, or ended
	// without saying how.
	ExitCode *int `json:"exit_code,omitzero"`

	// FinishTime is when the guest last ended on its own.
	FinishTime time.Time `json:"finish_time,omitzero"`

	// RestartCount is how many times in a row the restart policy has
	// started the instance again. A start a user asks for resets it.
	RestartCount int `json:"restart_count,omitzero"`

	// NextRestartTime is when a Restarting instance is due to start again.
	NextRestartTime time.Time `json:"next_restart_time,omitzero"`

	// Health is what the health check of the running instance has found, or
	// nil if it is not running or has no check.
	Health *Health `json:"health,omitzero"`
}

// InstanceSpec is the definition of an instance: what it boots, and with
// what. Empty fields take the daemon's defaults.
type InstanceSpec struct {
	Name     string `json:"name,omitzero"`
	Hostname string `json:"hostname,omitzero"`
	ImageRef string `json:"image_ref,omitzero"`

	// HypervisorType is the hypervisor the instance runs on. Empty means
	// Cloud Hypervisor.
	HypervisorType HypervisorType `json:"hypervisor_type,omitzero"`

	// HypervisorVersion is a version that hypervisor ships. Empty is its
	// default version, which HypervisorInfo lists first.
	HypervisorVersion string `json:"hypervisor_version,omitzero"`

	// KernelName is the kernel the guest boots. Empty means "default".
	KernelName string `json:"kernel_name,omitzero"`
	KernelArgs string `json:"kernel_args,omitzero"`

	VCPUs       int   `json:"vcpus,omitzero"`
	MemoryBytes int64 `json:"memory_bytes,omitzero"`

	// MaxVCPUs and MaxMemoryBytes are the most Instances.Resize can give the
	// running instance: it boots with room for them, which costs the host
	// nothing until it is used. Zero leaves no room. Firecracker cannot add
	// vCPUs to a running guest, so MaxVCPUs is Cloud Hypervisor's only.
	MaxVCPUs       int   `json:"max_vcpus,omitzero"`
	MaxMemoryBytes int64 `json:"max_memory_bytes,omitzero"`

	DiskBytes int64 `json:"disk_bytes,omitzero"`

	// DiskBytesPerSecond and DiskIOPS limit how fast each of the instance's
	// disks is read and written. UploadBytesPerSecond and
	// DownloadBytesPerSecond limit what the guest sends and receives on its
	// network. Zero is unlimited.
	DiskBytesPerSecond     int64 `json:"disk_bytes_per_second,omitzero"`
	DiskIOPS               int64 `json:"disk_iops,omitzero"`
	UploadBytesPerSecond   int64 `json:"upload_bytes_per_second,omitzero"`
	DownloadBytesPerSecond int64 `json:"download_bytes_per_second,omitzero"`

	// StandbyAfter is how long the instance may be idle before it is put on
	// standby: at least a minute, or zero for never.
	StandbyAfter time.Duration `json:"standby_after,omitzero"`

	// NetworkName is the network the instance joins. Empty means "default".
	NetworkName string `json:"network_name,omitzero"`
	StaticIP    string `json:"static_ip,omitzero"`

	Mounts []Mount           `json:"mounts,omitzero"`
	Env    map[string]string `json:"env,omitzero"`
	Cmd    []string          `json:"cmd,omitzero"`
	Labels map[string]string `json:"labels,omitzero"`

	RestartPolicy RestartPolicy `json:"restart_policy,omitzero"`

	// HealthCheck is how the instance's health is checked, overriding its
	// image's. Nil means the image's, if it declares one.
	HealthCheck *HealthCheck `json:"health_check,omitzero"`

	// InitMode is how the guest starts the command. Empty means auto.
	InitMode InitMode `json:"init_mode,omitzero"`

	// Ports are the guest ports published on the host.
	Ports []PortMapping `json:"ports,omitzero"`

	// RemoveOnExit deletes the instance once it stops, whether its guest
	// ended on its own or someone stopped it. The daemon does the deleting,
	// so it happens even if the client has gone. An instance that its
	// restart policy will start again is not deleted.
	RemoveOnExit bool `json:"remove_on_exit,omitzero"`
}

// InstanceState is where an instance is in its lifecycle.
type InstanceState string

// The instance states.
const (
	// InstanceStateStopped means the instance is defined but not running. A
	// new instance starts here.
	InstanceStateStopped InstanceState = "stopped"

	// InstanceStateStarting means a start is in progress.
	InstanceStateStarting InstanceState = "starting"

	// InstanceStateRunning means the guest is running.
	InstanceStateRunning InstanceState = "running"

	// InstanceStatePaused means the guest's vCPUs are halted, and it is kept
	// resident.
	InstanceStatePaused InstanceState = "paused"

	// InstanceStateStopping means a stop is in progress.
	InstanceStateStopping InstanceState = "stopping"

	// InstanceStateRestarting means the instance ended without being asked
	// to, and its restart policy will start it again at NextRestartTime.
	InstanceStateRestarting InstanceState = "restarting"

	// InstanceStateFailed means the last operation failed; StateError says
	// why.
	InstanceStateFailed InstanceState = "failed"

	// InstanceStateStandby means the guest is frozen to disk and its
	// hypervisor has ended. Starting it resumes it where it was.
	InstanceStateStandby InstanceState = "standby"
)

var instanceStates = enum[InstanceState, dicerdv1.InstanceState]{"instance state", map[InstanceState]dicerdv1.InstanceState{
	InstanceStateStopped:    dicerdv1.InstanceState_INSTANCE_STATE_STOPPED,
	InstanceStateStarting:   dicerdv1.InstanceState_INSTANCE_STATE_STARTING,
	InstanceStateRunning:    dicerdv1.InstanceState_INSTANCE_STATE_RUNNING,
	InstanceStatePaused:     dicerdv1.InstanceState_INSTANCE_STATE_PAUSED,
	InstanceStateStopping:   dicerdv1.InstanceState_INSTANCE_STATE_STOPPING,
	InstanceStateRestarting: dicerdv1.InstanceState_INSTANCE_STATE_RESTARTING,
	InstanceStateFailed:     dicerdv1.InstanceState_INSTANCE_STATE_FAILED,
	InstanceStateStandby:    dicerdv1.InstanceState_INSTANCE_STATE_STANDBY,
}}

// HypervisorType is the virtual machine monitor an instance runs on.
type HypervisorType string

// The hypervisor types.
const (
	HypervisorTypeCloudHypervisor HypervisorType = "cloud-hypervisor"
	HypervisorTypeFirecracker     HypervisorType = "firecracker"
)

var hypervisorTypes = enum[HypervisorType, dicerdv1.HypervisorType]{"hypervisor type", map[HypervisorType]dicerdv1.HypervisorType{
	HypervisorTypeCloudHypervisor: dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR,
	HypervisorTypeFirecracker:     dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER,
}}

// InitMode is how the guest starts an instance's command.
type InitMode string

// The init modes.
const (
	// InitModeAuto runs systemd as the machine's PID 1 if the command is the
	// systemd binary, and the command as InitModeExec does otherwise.
	InitModeAuto InitMode = "auto"

	// InitModeExec runs the command as PID 1 of its own PID namespace, as a
	// container runs it.
	InitModeExec InitMode = "exec"

	// InitModeSystemd runs the command, systemd, as the machine's PID 1.
	InitModeSystemd InitMode = "systemd"
)

var initModes = enum[InitMode, dicerdv1.InitMode]{"init mode", map[InitMode]dicerdv1.InitMode{
	InitModeAuto:    dicerdv1.InitMode_INIT_MODE_AUTO,
	InitModeExec:    dicerdv1.InitMode_INIT_MODE_EXEC,
	InitModeSystemd: dicerdv1.InitMode_INIT_MODE_SYSTEMD,
}}

// RestartPolicy is what the daemon does when an instance ends without being
// asked to: when its workload exits, its guest resets or its hypervisor
// dies. Restarts back off exponentially, from a second up to five minutes;
// an instance that stays up for ten minutes starts the count again.
type RestartPolicy struct {
	// Mode is when the instance is started again. Empty means no.
	Mode RestartMode `json:"mode,omitzero"`

	// MaxRetries is, for on-failure, how many restarts in a row are made
	// before the instance is left Failed. Zero means no limit.
	MaxRetries int `json:"max_retries,omitzero"`
}

// RestartMode is when an instance that ended without being asked to is
// started again.
type RestartMode string

// The restart modes.
const (
	// RestartModeNo leaves the instance as it ended.
	RestartModeNo RestartMode = "no"

	// RestartModeOnFailure restarts it unless it ended cleanly, by its
	// workload exiting 0 or its guest powering off.
	RestartModeOnFailure RestartMode = "on-failure"

	// RestartModeUnlessStopped restarts it however it ended, and starts it
	// when the daemon starts, unless a user stopped it.
	RestartModeUnlessStopped RestartMode = "unless-stopped"

	// RestartModeAlways restarts it however it ended, and starts it when the
	// daemon starts, even if a user stopped it.
	RestartModeAlways RestartMode = "always"
)

var restartModes = enum[RestartMode, dicerdv1.RestartMode]{"restart mode", map[RestartMode]dicerdv1.RestartMode{
	RestartModeNo:            dicerdv1.RestartMode_RESTART_MODE_NO,
	RestartModeOnFailure:     dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE,
	RestartModeUnlessStopped: dicerdv1.RestartMode_RESTART_MODE_UNLESS_STOPPED,
	RestartModeAlways:        dicerdv1.RestartMode_RESTART_MODE_ALWAYS,
}}

// Mount attaches a volume, a file or a tmpfs at Target in the guest.
type Mount struct {
	Type MountType `json:"type,omitzero"`

	// Source is the volume's name for a volume. A file and a tmpfs have
	// none.
	Source string `json:"source,omitzero"`

	// Target is the absolute path the mount appears at in the guest.
	Target string `json:"target,omitzero"`

	// ReadOnly mounts it read-only. A volume every instance mounts read-only
	// can be shared between them.
	ReadOnly bool `json:"read_only,omitzero"`

	// Content is a file's contents. The daemon keeps them with the instance
	// and writes them into the guest at each start. An instance's file
	// mounts can hold at most 1 MiB between them. FileMount reads them from
	// a file on this machine.
	Content []byte `json:"content,omitzero"`

	// Mode is a file's permission bits in the guest, where root owns it.
	// Zero is 0644.
	Mode fs.FileMode `json:"mode,omitzero"`
}

// FileMount returns a mount that puts a copy of the file at path, on this
// machine, at target in the guest, with the file's permission bits.
func FileMount(path, target string) (Mount, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Mount{}, err
	}
	if !info.Mode().IsRegular() {
		return Mount{}, fmt.Errorf("%s is not a regular file: only a file can be mounted", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Mount{}, err
	}

	return Mount{Type: MountTypeFile, Target: target, Content: content, Mode: info.Mode().Perm()}, nil
}

// MountType is what a Mount attaches.
type MountType string

// The mount types.
const (
	// MountTypeVolume is a named volume: persistent storage, as a disk of
	// its own.
	MountTypeVolume MountType = "volume"

	// MountTypeFile is a file whose contents the client gives, written
	// into the guest at each start.
	MountTypeFile MountType = "file"

	// MountTypeTmpfs is an empty in-memory filesystem, lost when the guest
	// stops.
	MountTypeTmpfs MountType = "tmpfs"
)

var mountTypes = enum[MountType, dicerdv1.MountType]{"mount type", map[MountType]dicerdv1.MountType{
	MountTypeVolume: dicerdv1.MountType_MOUNT_TYPE_VOLUME,
	MountTypeFile:   dicerdv1.MountType_MOUNT_TYPE_FILE,
	MountTypeTmpfs:  dicerdv1.MountType_MOUNT_TYPE_TMPFS,
}}

// PortMapping publishes a guest port on the host, so that the guest can be
// reached from outside it. Traffic to the host's loopback addresses is not
// relayed.
type PortMapping struct {
	// HostIP is the host address to publish on. Empty means every address
	// the host has.
	HostIP    string `json:"host_ip,omitzero"`
	HostPort  int    `json:"host_port,omitzero"`
	GuestPort int    `json:"guest_port,omitzero"`

	// Protocol is the transport published. Empty means TCP.
	Protocol Protocol `json:"protocol,omitzero"`
}

// Protocol is the transport a port mapping publishes.
type Protocol string

// The protocols.
const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

var protocols = enum[Protocol, dicerdv1.Protocol]{"protocol", map[Protocol]dicerdv1.Protocol{
	ProtocolTCP: dicerdv1.Protocol_PROTOCOL_TCP,
	ProtocolUDP: dicerdv1.Protocol_PROTOCOL_UDP,
}}

// CreateOptions are how Instances.Create creates an instance.
type CreateOptions struct {
	// Start boots the instance once it is defined.
	Start bool

	// PullPolicy is when the image is pulled. Empty means
	// PullPolicyMissing.
	PullPolicy PullPolicy
}

// InstanceUpdate is a change to a stopped instance's definition. Only the
// fields that are set change: a pointer that is not nil, an enumeration
// that is not empty, and a slice or map that is not empty, which replaces
// the existing value whole. Ptr makes the pointers:
//
//	update := dicer.InstanceUpdate{VCPUs: dicer.Ptr(4), Labels: map[string]string{"tier": "web"}}
type InstanceUpdate struct {
	ImageRef          *string
	HypervisorType    HypervisorType
	HypervisorVersion *string
	KernelName        *string
	KernelArgs        *string
	VCPUs             *int
	MemoryBytes       *int64

	// MaxVCPUs and MaxMemoryBytes of zero remove the maximum.
	MaxVCPUs       *int
	MaxMemoryBytes *int64

	// DiskBytes grows the overlay disk at the instance's next start. It
	// cannot shrink, so a size smaller than the overlay disk is refused.
	DiskBytes *int64

	// The rate limits, of which zero removes the limit.
	DiskBytesPerSecond     *int64
	DiskIOPS               *int64
	UploadBytesPerSecond   *int64
	DownloadBytesPerSecond *int64

	// StandbyAfter of zero is never.
	StandbyAfter *time.Duration

	NetworkName   *string
	StaticIP      *string
	Hostname      *string
	RestartPolicy *RestartPolicy
	HealthCheck   *HealthCheck
	InitMode      InitMode
	RemoveOnExit  *bool

	Mounts []Mount
	Env    map[string]string
	Cmd    []string
	Labels map[string]string
	Ports  []PortMapping
}

// ForkOptions are the identity a fork is given, by Instances.Fork and
// Snapshots.Fork.
type ForkOptions struct {
	// Name is the new instance's name.
	Name string

	// NetworkName and StaticIP are the network the new instance joins, and
	// its address on it. Empty means the original's network, and an address
	// it assigns.
	NetworkName string
	StaticIP    string

	// Ports are the host ports the new instance publishes. The original's
	// are not copied: two instances cannot publish the same host port.
	Ports []PortMapping
}

// DeleteOptions are how a resource is deleted, by Instances.Delete and
// Images.Delete.
type DeleteOptions struct {
	// Force stops a running instance before deleting it, and deletes an
	// image even if an instance is defined to boot from it.
	Force bool
}

// ResizeOptions are what Instances.Resize gives a running instance. A field
// left zero stays as it is, and at least one must be set.
type ResizeOptions struct {
	VCPUs       int
	MemoryBytes int64
}

// Process is a process running in an instance's guest.
type Process struct {
	PID  int `json:"pid,omitzero"`
	PPID int `json:"ppid,omitzero"`

	// User is the user the process runs as, by name, or by UID if the guest
	// has no name for it.
	User string `json:"user,omitzero"`

	// State is the state code: R running, S sleeping, D waiting on I/O, Z
	// zombie, T stopped, and so on.
	State string `json:"state,omitzero"`

	// Name is the command's name, which the kernel keeps even when the
	// command line is gone, as it is for a zombie.
	Name string `json:"name,omitzero"`

	// Command is the command line. It is empty for a zombie.
	Command []string `json:"command,omitzero"`

	// StartTime is when the process started, by the guest's clock.
	StartTime time.Time `json:"start_time,omitzero"`

	// CPUTime is the CPU time used in total, in user and kernel mode.
	CPUTime time.Duration `json:"cpu_time,omitzero"`

	ResidentMemoryBytes int64 `json:"resident_memory_bytes,omitzero"`
}

// Create defines an instance, without starting it unless opts says to. It
// first pulls the image as opts.PullPolicy says, reporting no progress: to
// show a pull's progress, call Images.Pull first. Nothing is defined if the
// image cannot be had.
func (s *Instances) Create(ctx context.Context, spec InstanceSpec, opts CreateOptions) (Instance, error) {
	req, err := createInstanceRequest(spec, opts)
	if err != nil {
		return Instance{}, err
	}
	return instanceOf(s.api.CreateInstance(ctx, req))
}

// Update changes the definition of a stopped instance.
func (s *Instances) Update(ctx context.Context, name string, update InstanceUpdate) (Instance, error) {
	req, err := updateInstanceRequest(name, update)
	if err != nil {
		return Instance{}, err
	}
	return instanceOf(s.api.UpdateInstance(ctx, req))
}

// Rename changes a stopped instance's name. The instance keeps its ID, its
// disks and its address.
func (s *Instances) Rename(ctx context.Context, name, newName string) (Instance, error) {
	return instanceOf(s.api.RenameInstance(ctx, &dicerdv1.RenameInstanceRequest{Name: name, NewName: newName}))
}

// Start boots a defined instance, or resumes one on standby.
func (s *Instances) Start(ctx context.Context, name string) (Instance, error) {
	return instanceOf(s.api.StartInstance(ctx, &dicerdv1.StartInstanceRequest{Name: name}))
}

// Stop shuts a running instance down, keeping its definition, overlay disk
// and address.
func (s *Instances) Stop(ctx context.Context, name string) (Instance, error) {
	return instanceOf(s.api.StopInstance(ctx, &dicerdv1.StopInstanceRequest{Name: name}))
}

// Pause halts a running instance's vCPUs, keeping it resident.
func (s *Instances) Pause(ctx context.Context, name string) (Instance, error) {
	return instanceOf(s.api.PauseInstance(ctx, &dicerdv1.PauseInstanceRequest{Name: name}))
}

// Resume continues a paused instance.
func (s *Instances) Resume(ctx context.Context, name string) (Instance, error) {
	return instanceOf(s.api.ResumeInstance(ctx, &dicerdv1.ResumeInstanceRequest{Name: name}))
}

// Standby freezes a running or paused instance to disk and ends its
// hypervisor, freeing the CPU and memory committed to it. Start resumes it
// where it was; Stop discards what was frozen.
func (s *Instances) Standby(ctx context.Context, name string) (Instance, error) {
	return instanceOf(s.api.StandbyInstance(ctx, &dicerdv1.StandbyInstanceRequest{Name: name}))
}

// Fork creates an instance as a copy of another, as Snapshots.Create
// followed by Snapshots.Fork would, but keeps no snapshot. A running or
// paused instance is paused while its memory is written, and its fork runs.
// A stopped instance's fork is stopped.
func (s *Instances) Fork(ctx context.Context, name string, opts ForkOptions) (Instance, error) {
	req, err := forkInstanceRequest(name, opts)
	if err != nil {
		return Instance{}, err
	}
	return instanceOf(s.api.ForkInstance(ctx, req))
}

// Resize changes a running instance's vCPUs and memory without restarting
// it, within its MaxVCPUs and MaxMemoryBytes, and its definition with them.
// It fails with ErrResourceExhausted if the host has no room to grow it.
func (s *Instances) Resize(ctx context.Context, name string, opts ResizeOptions) (Instance, error) {
	return instanceOf(s.api.ResizeInstance(ctx, resizeInstanceRequest(name, opts)))
}

// Delete removes an instance and its overlay disk. It refuses a running
// instance with ErrFailedPrecondition, unless opts.Force is set.
func (s *Instances) Delete(ctx context.Context, name string, opts DeleteOptions) error {
	_, err := s.api.DeleteInstance(ctx, &dicerdv1.DeleteInstanceRequest{Name: name, Force: opts.Force})
	return fromStatus(err)
}

// List returns every instance.
func (s *Instances) List(ctx context.Context) ([]Instance, error) {
	resp, err := s.api.ListInstances(ctx, &dicerdv1.ListInstancesRequest{})
	if err != nil {
		return nil, fromStatus(err)
	}
	return convertAll(resp.GetInstances(), instanceFromProto), nil
}

// Get returns one instance, or ErrNotFound.
func (s *Instances) Get(ctx context.Context, name string) (Instance, error) {
	return instanceOf(s.api.GetInstance(ctx, &dicerdv1.GetInstanceRequest{Name: name}))
}

// Processes returns the processes running in a running instance, as its
// guest sees them, in PID order. Kernel threads are left out. It waits for
// a guest that is still booting.
func (s *Instances) Processes(ctx context.Context, name string) ([]Process, error) {
	resp, err := s.api.ListInstanceProcesses(ctx, &dicerdv1.ListInstanceProcessesRequest{Name: name})
	if err != nil {
		return nil, fromStatus(err)
	}
	return convertAll(resp.GetProcesses(), processFromProto), nil
}

// instanceOf returns the instance a call returned, or the error it failed
// with.
func instanceOf(p *dicerdv1.Instance, err error) (Instance, error) {
	if err != nil {
		return Instance{}, fromStatus(err)
	}
	return instanceFromProto(p), nil
}

// instanceFromProto returns the instance p describes.
func instanceFromProto(p *dicerdv1.Instance) Instance {
	instance := Instance{
		ID: p.GetId(),
		InstanceSpec: InstanceSpec{
			Name:                   p.GetName(),
			Hostname:               p.GetHostname(),
			ImageRef:               p.GetImageRef(),
			HypervisorType:         hypervisorTypes.fromProto(p.GetHypervisorType()),
			HypervisorVersion:      p.GetHypervisorVersion(),
			KernelName:             p.GetKernelName(),
			KernelArgs:             p.GetKernelArgs(),
			VCPUs:                  int(p.GetVcpus()),
			MemoryBytes:            p.GetMemoryBytes(),
			MaxVCPUs:               int(p.GetMaxVcpus()),
			MaxMemoryBytes:         p.GetMaxMemoryBytes(),
			DiskBytes:              p.GetDiskBytes(),
			DiskBytesPerSecond:     p.GetDiskBytesPerSecond(),
			DiskIOPS:               p.GetDiskIops(),
			UploadBytesPerSecond:   p.GetUploadBytesPerSecond(),
			DownloadBytesPerSecond: p.GetDownloadBytesPerSecond(),
			StandbyAfter:           durationFromProto(p.GetStandbyAfter()),
			NetworkName:            p.GetNetworkName(),
			StaticIP:               p.GetStaticIp(),
			Mounts:                 convertAll(p.GetMounts(), mountFromProto),
			Env:                    p.GetEnv(),
			Cmd:                    p.GetCmd(),
			Labels:                 p.GetLabels(),
			RestartPolicy:          restartPolicyFromProto(p.GetRestartPolicy()),
			HealthCheck:            healthCheckFromProto(p.GetHealthCheck()),
			InitMode:               initModes.fromProto(p.GetInitMode()),
			Ports:                  convertAll(p.GetPorts(), portMappingFromProto),
			RemoveOnExit:           p.GetRemoveOnExit(),
		},
		CreateTime:      timeFromProto(p.GetCreateTime()),
		UpdateTime:      timeFromProto(p.GetUpdateTime()),
		State:           instanceStates.fromProto(p.GetState()),
		StateError:      p.GetStateError(),
		HypervisorPID:   int(p.GetHypervisorPid()),
		VsockCID:        p.GetVsockCid(),
		IP:              p.GetIp(),
		MAC:             p.GetMac(),
		StartTime:       timeFromProto(p.GetStartTime()),
		FinishTime:      timeFromProto(p.GetFinishTime()),
		RestartCount:    int(p.GetRestartCount()),
		NextRestartTime: timeFromProto(p.GetNextRestartTime()),
		Health:          healthFromProto(p.GetHealth()),
	}
	if p.ExitCode != nil {
		instance.ExitCode = Ptr(int(p.GetExitCode()))
	}

	return instance
}

// createInstanceRequest returns the request that creates the instance spec
// defines, as opts says.
func createInstanceRequest(spec InstanceSpec, opts CreateOptions) (*dicerdv1.CreateInstanceRequest, error) {
	req := &dicerdv1.CreateInstanceRequest{
		Name:                   spec.Name,
		ImageRef:               spec.ImageRef,
		HypervisorVersion:      spec.HypervisorVersion,
		KernelName:             spec.KernelName,
		KernelArgs:             spec.KernelArgs,
		Vcpus:                  int32(spec.VCPUs),
		MemoryBytes:            spec.MemoryBytes,
		MaxVcpus:               int32(spec.MaxVCPUs),
		MaxMemoryBytes:         spec.MaxMemoryBytes,
		DiskBytes:              spec.DiskBytes,
		DiskBytesPerSecond:     spec.DiskBytesPerSecond,
		DiskIops:               spec.DiskIOPS,
		UploadBytesPerSecond:   spec.UploadBytesPerSecond,
		DownloadBytesPerSecond: spec.DownloadBytesPerSecond,
		StandbyAfter:           durationToProto(spec.StandbyAfter),
		NetworkName:            spec.NetworkName,
		StaticIp:               spec.StaticIP,
		Env:                    spec.Env,
		Cmd:                    spec.Cmd,
		Labels:                 spec.Labels,
		Hostname:               spec.Hostname,
		Start:                  opts.Start,
		RemoveOnExit:           spec.RemoveOnExit,
	}

	var err error
	if req.HypervisorType, err = hypervisorTypes.toProto(spec.HypervisorType); err != nil {
		return nil, err
	}
	if req.InitMode, err = initModes.toProto(spec.InitMode); err != nil {
		return nil, err
	}
	if req.PullPolicy, err = pullPolicies.toProto(opts.PullPolicy); err != nil {
		return nil, err
	}
	if req.Mounts, err = mountsToProto(spec.Mounts); err != nil {
		return nil, err
	}
	if req.Ports, err = portMappingsToProto(spec.Ports); err != nil {
		return nil, err
	}
	if spec.RestartPolicy != (RestartPolicy{}) {
		if req.RestartPolicy, err = restartPolicyToProto(spec.RestartPolicy); err != nil {
			return nil, err
		}
	}
	if spec.HealthCheck != nil {
		if req.HealthCheck, err = healthCheckToProto(*spec.HealthCheck); err != nil {
			return nil, err
		}
	}

	return req, nil
}

// updateInstanceRequest returns the request that makes update to the
// instance name.
func updateInstanceRequest(name string, update InstanceUpdate) (*dicerdv1.UpdateInstanceRequest, error) {
	req := &dicerdv1.UpdateInstanceRequest{
		Name:                   name,
		ImageRef:               update.ImageRef,
		HypervisorVersion:      update.HypervisorVersion,
		KernelName:             update.KernelName,
		KernelArgs:             update.KernelArgs,
		MemoryBytes:            update.MemoryBytes,
		MaxMemoryBytes:         update.MaxMemoryBytes,
		DiskBytes:              update.DiskBytes,
		DiskBytesPerSecond:     update.DiskBytesPerSecond,
		DiskIops:               update.DiskIOPS,
		UploadBytesPerSecond:   update.UploadBytesPerSecond,
		DownloadBytesPerSecond: update.DownloadBytesPerSecond,
		NetworkName:            update.NetworkName,
		StaticIp:               update.StaticIP,
		Hostname:               update.Hostname,
		RemoveOnExit:           update.RemoveOnExit,
		Env:                    update.Env,
		Cmd:                    update.Cmd,
		Labels:                 update.Labels,
	}
	if update.VCPUs != nil {
		req.Vcpus = Ptr(int32(*update.VCPUs))
	}
	if update.MaxVCPUs != nil {
		req.MaxVcpus = Ptr(int32(*update.MaxVCPUs))
	}
	if update.StandbyAfter != nil {
		// Present even at zero, which the API reads as never.
		req.StandbyAfter = durationpb.New(*update.StandbyAfter)
	}

	var err error
	if req.HypervisorType, err = hypervisorTypes.toProto(update.HypervisorType); err != nil {
		return nil, err
	}
	if req.InitMode, err = initModes.toProto(update.InitMode); err != nil {
		return nil, err
	}
	if req.Mounts, err = mountsToProto(update.Mounts); err != nil {
		return nil, err
	}
	if req.Ports, err = portMappingsToProto(update.Ports); err != nil {
		return nil, err
	}
	if update.RestartPolicy != nil {
		if req.RestartPolicy, err = restartPolicyToProto(*update.RestartPolicy); err != nil {
			return nil, err
		}
	}
	if update.HealthCheck != nil {
		if req.HealthCheck, err = healthCheckToProto(*update.HealthCheck); err != nil {
			return nil, err
		}
	}

	return req, nil
}

// forkInstanceRequest returns the request that forks the instance name.
func forkInstanceRequest(name string, opts ForkOptions) (*dicerdv1.ForkInstanceRequest, error) {
	ports, err := portMappingsToProto(opts.Ports)
	if err != nil {
		return nil, err
	}

	return &dicerdv1.ForkInstanceRequest{
		Name:        name,
		ForkName:    opts.Name,
		NetworkName: opts.NetworkName,
		StaticIp:    opts.StaticIP,
		Ports:       ports,
	}, nil
}

// resizeInstanceRequest returns the request that resizes the instance name.
func resizeInstanceRequest(name string, opts ResizeOptions) *dicerdv1.ResizeInstanceRequest {
	req := &dicerdv1.ResizeInstanceRequest{Name: name}
	if opts.VCPUs != 0 {
		req.Vcpus = Ptr(int32(opts.VCPUs))
	}
	if opts.MemoryBytes != 0 {
		req.MemoryBytes = &opts.MemoryBytes
	}

	return req
}

// restartPolicyFromProto returns the policy p describes.
func restartPolicyFromProto(p *dicerdv1.RestartPolicy) RestartPolicy {
	return RestartPolicy{Mode: restartModes.fromProto(p.GetMode()), MaxRetries: int(p.GetMaxRetries())}
}

// restartPolicyToProto returns policy as the API carries it.
func restartPolicyToProto(policy RestartPolicy) (*dicerdv1.RestartPolicy, error) {
	mode, err := restartModes.toProto(policy.Mode)
	if err != nil {
		return nil, err
	}

	return &dicerdv1.RestartPolicy{Mode: mode, MaxRetries: int32(policy.MaxRetries)}, nil
}

// mountFromProto returns the mount p describes.
func mountFromProto(p *dicerdv1.Mount) Mount {
	return Mount{
		Type:     mountTypes.fromProto(p.GetType()),
		Source:   p.GetSource(),
		Target:   p.GetTarget(),
		ReadOnly: p.GetReadOnly(),
		Content:  p.GetContent(),
		Mode:     fs.FileMode(p.GetMode()),
	}
}

// mountsToProto returns mounts as the API carries them.
func mountsToProto(mounts []Mount) ([]*dicerdv1.Mount, error) {
	out := make([]*dicerdv1.Mount, 0, len(mounts))
	for _, m := range mounts {
		mountType, err := mountTypes.toProto(m.Type)
		if err != nil {
			return nil, err
		}
		out = append(out, &dicerdv1.Mount{
			Type:     mountType,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
			Content:  m.Content,
			Mode:     uint32(m.Mode.Perm()),
		})
	}

	return out, nil
}

// portMappingFromProto returns the port mapping p describes.
func portMappingFromProto(p *dicerdv1.PortMapping) PortMapping {
	return PortMapping{
		HostIP:    p.GetHostIp(),
		HostPort:  int(p.GetHostPort()),
		GuestPort: int(p.GetGuestPort()),
		Protocol:  protocols.fromProto(p.GetProtocol()),
	}
}

// portMappingsToProto returns ports as the API carries them.
func portMappingsToProto(ports []PortMapping) ([]*dicerdv1.PortMapping, error) {
	out := make([]*dicerdv1.PortMapping, 0, len(ports))
	for _, p := range ports {
		protocol, err := protocols.toProto(p.Protocol)
		if err != nil {
			return nil, err
		}
		out = append(out, &dicerdv1.PortMapping{
			HostIp:    p.HostIP,
			HostPort:  uint32(p.HostPort),
			GuestPort: uint32(p.GuestPort),
			Protocol:  protocol,
		})
	}

	return out, nil
}

// processFromProto returns the process p describes.
func processFromProto(p *dicerdv1.Process) Process {
	return Process{
		PID:                 int(p.GetPid()),
		PPID:                int(p.GetPpid()),
		User:                p.GetUser(),
		State:               p.GetState(),
		Name:                p.GetName(),
		Command:             p.GetCommand(),
		StartTime:           timeFromProto(p.GetStartTime()),
		CPUTime:             durationFromProto(p.GetCpuTime()),
		ResidentMemoryBytes: p.GetResidentMemoryBytes(),
	}
}
