// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/naming"
)

// Instance is a virtual machine. Spec is the desired state, persisted across
// reboots. Status is the observed state, kept in the runtime directory and
// empty after a reboot.
type Instance struct {
	Spec   InstanceSpec   `json:"spec"`
	Status InstanceStatus `json:"status"`
}

// InstanceSpec is the persistent definition of a virtual machine. Referenced
// kernels, networks and mounts are resolved at each start.
type InstanceSpec struct {
	ID                string         `yaml:"id" json:"id"`
	Name              string         `yaml:"name" json:"name"`
	Hostname          string         `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	ImageRef          string         `yaml:"image_ref" json:"image_ref"`
	HypervisorType    HypervisorType `yaml:"hypervisor_type,omitempty" json:"hypervisor_type,omitempty"`
	HypervisorVersion string         `yaml:"hypervisor_version,omitempty" json:"hypervisor_version,omitempty"`
	KernelName        string         `yaml:"kernel_name" json:"kernel_name"`
	KernelArgs        string         `yaml:"kernel_args,omitempty" json:"kernel_args,omitempty"`
	VCPUs             int            `yaml:"vcpus" json:"vcpus"`
	MemoryBytes       int64          `yaml:"memory_bytes" json:"memory_bytes"`
	DiskBytes         int64          `yaml:"disk_bytes" json:"disk_bytes"`
	NetworkName       string         `yaml:"network_name" json:"network_name"`
	StaticIP          string         `yaml:"static_ip,omitempty" json:"static_ip,omitempty"`

	// MaxVCPUs and MaxMemoryBytes are the most the instance can be resized
	// to while it runs: it boots with room for them. Zero leaves no room.
	MaxVCPUs       int   `yaml:"max_vcpus,omitempty" json:"max_vcpus,omitempty"`
	MaxMemoryBytes int64 `yaml:"max_memory_bytes,omitempty" json:"max_memory_bytes,omitempty"`

	// DiskBytesPerSecond and DiskIOPS limit the bytes and operations per
	// second each of the instance's disks is read and written at.
	// UploadBytesPerSecond and DownloadBytesPerSecond limit the bytes per
	// second its guest sends and receives. Zero is unlimited.
	DiskBytesPerSecond     int64 `yaml:"disk_bytes_per_second,omitempty" json:"disk_bytes_per_second,omitempty"`
	DiskIOPS               int64 `yaml:"disk_iops,omitempty" json:"disk_iops,omitempty"`
	UploadBytesPerSecond   int64 `yaml:"upload_bytes_per_second,omitempty" json:"upload_bytes_per_second,omitempty"`
	DownloadBytesPerSecond int64 `yaml:"download_bytes_per_second,omitempty" json:"download_bytes_per_second,omitempty"`

	Ports  []PortMapping     `yaml:"ports,omitempty" json:"ports,omitempty"`
	Mounts []Mount           `yaml:"mounts,omitempty" json:"mounts,omitempty"`
	Env    map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Cmd    []string          `yaml:"cmd,omitempty" json:"cmd,omitempty"`

	// InitMode is how the guest starts the command: auto, exec or systemd.
	// Empty is auto: systemd if the command is systemd, else exec.
	InitMode InitMode `yaml:"init_mode,omitempty" json:"init_mode,omitempty"`

	Labels  map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Restart RestartPolicy     `yaml:"restart,omitempty" json:"restart,omitzero"`

	// HealthCheck is how the instance's health is checked, overriding its
	// image's; a Disabled one switches the image's off. Nil means the
	// image's, if it declares one.
	HealthCheck *HealthCheck `yaml:"healthcheck,omitempty" json:"health_check,omitempty"`

	CreatedAt time.Time `yaml:"created_at" json:"created_at,omitzero"`
	UpdatedAt time.Time `yaml:"updated_at" json:"updated_at,omitzero"`

	// RemoveOnExit deletes the instance once it stops, unless its restart
	// policy will start it again.
	RemoveOnExit bool `yaml:"remove_on_exit,omitempty" json:"remove_on_exit,omitempty"`

	// StoppedByUser records that a user stopped the instance and has not
	// started it since. An unless-stopped instance is not started at boot
	// while it is set.
	StoppedByUser bool `yaml:"stopped_by_user,omitempty" json:"stopped_by_user,omitempty"`
}

// Resources returns what the instance asks for.
func (s InstanceSpec) Resources() Resources {
	return Resources{VCPUs: s.VCPUs, MemoryBytes: s.MemoryBytes}
}

// MaxResources returns the most the instance can hold: its maximums where
// set, otherwise what it asks for.
func (s InstanceSpec) MaxResources() Resources {
	return Resources{VCPUs: max(s.VCPUs, s.MaxVCPUs), MemoryBytes: max(s.MemoryBytes, s.MaxMemoryBytes)}
}

// Validate returns an invalid argument error if the instance cannot be run
// as defined, as far as the definition alone can tell: an invalid name or
// hostname, no image, a size it cannot have, a maximum below what it asks
// for or that its hypervisor cannot honour, a negative rate limit, a request
// to be deleted when it stops that its restart policy contradicts, or
// invalid or clashing ports or mounts.
func (s InstanceSpec) Validate() error {
	if err := naming.Validate(s.Name); err != nil {
		return err
	}
	if err := naming.ValidateHostname(s.Hostname); err != nil {
		return err
	}

	switch {
	case s.ImageRef == "":
		return errdefs.InvalidArgument("an instance needs an image")
	case s.VCPUs <= 0:
		return errdefs.InvalidArgument("an instance needs at least 1 vCPU")
	case s.MemoryBytes <= 0:
		return errdefs.InvalidArgument("an instance needs more than 0 bytes of memory")
	case s.DiskBytes <= 0:
		return errdefs.InvalidArgument("an instance needs a disk of more than 0 bytes")
	case s.MaxVCPUs < 0 || s.MaxVCPUs > 0 && s.MaxVCPUs < s.VCPUs:
		return errdefs.InvalidArgument("max_vcpus %d is below the instance's %s",
			s.MaxVCPUs, humanize.Count(s.VCPUs, "vCPU"))
	case s.MaxMemoryBytes < 0 || s.MaxMemoryBytes > 0 && s.MaxMemoryBytes < s.MemoryBytes:
		return errdefs.InvalidArgument("max_memory_bytes %s is below the instance's %s memory",
			humanize.Bytes(s.MaxMemoryBytes), humanize.Bytes(s.MemoryBytes))
	case s.MaxVCPUs > 0 && s.EffectiveHypervisorType() == HypervisorTypeFirecracker:
		return errdefs.InvalidArgument("firecracker cannot add vCPUs to a running guest: leave max_vcpus unset, " +
			"or use cloud-hypervisor")
	case s.DiskBytesPerSecond < 0 || s.DiskIOPS < 0 || s.UploadBytesPerSecond < 0 || s.DownloadBytesPerSecond < 0:
		return errdefs.InvalidArgument("a rate limit cannot be negative: give 0 for no limit")
	case s.RemoveOnExit && s.Restart.Restarts():
		return errdefs.InvalidArgument(
			"an instance cannot be deleted when it stops and restarted when it stops: "+
				"the restart policy is %s, so drop it or drop the request to delete it", s.Restart)
	}

	if err := validatePorts(s.Ports); err != nil {
		return err
	}
	return validateMounts(s.Mounts)
}

// VolumeMount returns the mount by which the instance attaches the named
// volume, and false if it attaches none.
func (s InstanceSpec) VolumeMount(name string) (Mount, bool) {
	for _, m := range s.Mounts {
		if m.Type == MountTypeVolume && m.Source == name {
			return m, true
		}
	}

	return Mount{}, false
}

// EffectiveHypervisorType returns the hypervisor the instance runs on,
// filling in the default for a spec that names none.
func (s InstanceSpec) EffectiveHypervisorType() HypervisorType {
	if s.HypervisorType == "" {
		return DefaultHypervisorType
	}

	return s.HypervisorType
}

// InstanceStatus is the observed state of an instance. It lives under the
// runtime directory, so a host reboot discards it.
type InstanceStatus struct {
	InstanceID string        `json:"instance_id,omitempty"`
	State      InstanceState `json:"state"`
	StateError string        `json:"state_error,omitempty"`

	VMMPID               *int   `json:"hypervisor_pid,omitempty"`
	HypervisorSocketPath string `json:"hypervisor_socket_path,omitempty"`
	HypervisorVersion    string `json:"hypervisor_version,omitempty"`
	VsockCID             int64  `json:"vsock_cid,omitempty"`
	VsockPath            string `json:"vsock_path,omitempty"`

	// VCPUs and MemoryBytes are what the instance was admitted with. The
	// spec may have been edited since.
	VCPUs       int   `json:"vcpus,omitempty"`
	MemoryBytes int64 `json:"memory_bytes,omitempty"`

	// ImageDigest is the image the guest booted from.
	ImageDigest string `json:"image_digest,omitempty"`

	// IP and MAC are the instance's address, held while it is defined.
	IP  string `json:"ip,omitempty"`
	MAC string `json:"mac,omitempty"`

	// HealthCheck is the check this run was started with, or nil for none.
	HealthCheck *HealthCheck `json:"health_check,omitempty"`

	// Health is what that check has found, or nil if there is none.
	Health *Health `json:"health,omitempty"`

	StartedAt time.Time `json:"started_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`

	// ExitCode is the exit code the guest reported when it last ended on
	// its own, if it reported one.
	ExitCode *int `json:"exit_code,omitempty"`

	// FinishedAt is when the guest last ended without being asked to.
	FinishedAt time.Time `json:"finished_at,omitzero"`

	// RestartCount is how many times in a row the restart policy has
	// started the instance again. A start a user asks for resets it.
	RestartCount int `json:"restart_count,omitempty"`

	// NextRestartAt is when a Restarting instance is due to start again.
	NextRestartAt time.Time `json:"next_restart_at,omitzero"`
}

// HeldResources returns what the instance holds of the host's CPU and
// memory, which is nothing unless its state says it holds anything.
func (s InstanceStatus) HeldResources() Resources {
	if !s.State.HoldsResources() {
		return Resources{}
	}

	return Resources{VCPUs: s.VCPUs, MemoryBytes: s.MemoryBytes}
}

// InstanceState is the lifecycle state of an instance.
type InstanceState string

const (
	// InstanceStateStopped means the instance is defined but not running. A freshly
	// created instance starts here.
	InstanceStateStopped InstanceState = "Stopped"

	// InstanceStateStarting means a start is in progress. An instance found in this
	// state at daemon boot crashed mid-start and is cleaned up.
	InstanceStateStarting InstanceState = "Starting"

	// InstanceStateRunning means the VM is executing.
	InstanceStateRunning InstanceState = "Running"

	// InstanceStatePaused means the vCPUs are halted but the VM is resident.
	InstanceStatePaused InstanceState = "Paused"

	// InstanceStateStandby means the guest is frozen to disk, its VMM ended:
	// it holds no CPU or memory, but keeps its address, host ports and
	// writable volumes, and a start resumes it where it was.
	InstanceStateStandby InstanceState = "Standby"

	// InstanceStateStopping means a shutdown is in progress.
	InstanceStateStopping InstanceState = "Stopping"

	// InstanceStateRestarting means the instance ended without being asked to, and
	// its restart policy will start it again at
	// InstanceStatus.NextRestartAt. It holds nothing in the meantime.
	InstanceStateRestarting InstanceState = "Restarting"

	// InstanceStateFailed means the last operation failed; see
	// InstanceStatus.StateError.
	InstanceStateFailed InstanceState = "Failed"
)

// InstanceStates returns every lifecycle state, in the order an instance
// normally moves through them.
func InstanceStates() []InstanceState {
	return []InstanceState{
		InstanceStateStopped,
		InstanceStateStarting,
		InstanceStateRunning,
		InstanceStatePaused,
		InstanceStateStandby,
		InstanceStateStopping,
		InstanceStateRestarting,
		InstanceStateFailed,
	}
}

// allowedTransitions maps each state to the states it may move to.
var allowedTransitions = map[InstanceState][]InstanceState{
	InstanceStateStopped: {InstanceStateStarting},
	InstanceStateStarting: {
		InstanceStateRunning, InstanceStateRestarting, InstanceStateFailed,
	},
	InstanceStateRunning: {
		InstanceStatePaused, InstanceStateStopping, InstanceStateStopped,
		InstanceStateRestarting, InstanceStateFailed,
	},
	InstanceStatePaused: {
		InstanceStateRunning, InstanceStateStopping, InstanceStateStopped,
		InstanceStateRestarting, InstanceStateFailed,
	},
	InstanceStateStandby:  {InstanceStateStarting},
	InstanceStateStopping: {InstanceStateStopped, InstanceStateFailed},
	InstanceStateRestarting: {
		InstanceStateStarting, InstanceStateStopping, InstanceStateFailed,
	},
	InstanceStateFailed: {
		InstanceStateStarting, InstanceStateStopping, InstanceStateStopped,
	},
}

// CanTransitionTo reports whether a transition to target is allowed.
func (s InstanceState) CanTransitionTo(target InstanceState) bool {
	return slices.Contains(allowedTransitions[s], target)
}

// HoldsResources reports whether an instance in the state holds the CPU and
// memory it was admitted with: starting, running or paused.
func (s InstanceState) HoldsResources() bool {
	return s == InstanceStateStarting || s.IsActive()
}

// HoldsPortsAndVolumes reports whether an instance in the state keeps its
// published host ports and writable volumes to itself: one that holds
// resources, is stopping, or is on standby, to resume with them.
func (s InstanceState) HoldsPortsAndVolumes() bool {
	return s.HoldsResources() || s == InstanceStateStopping || s == InstanceStateStandby
}

// IsActive reports whether the state implies a live VMM.
func (s InstanceState) IsActive() bool {
	return s == InstanceStateRunning || s == InstanceStatePaused
}

// String returns the state as it is written: "Running".
func (s InstanceState) String() string { return string(s) }

// Lowercase returns the state as a sentence says it: "running", not
// "Running".
func (s InstanceState) Lowercase() string { return strings.ToLower(string(s)) }

// InitMode is how the guest starts an instance's command.
type InitMode string

const (
	// InitModeAuto has dicer-init decide, once the root filesystem is mounted:
	// systemd if the command is systemd, else exec.
	InitModeAuto InitMode = "auto"

	// InitModeExec runs the command as PID 1 of its own PID namespace.
	InitModeExec InitMode = "exec"

	// InitModeSystemd hands the machine's PID 1 to systemd itself.
	InitModeSystemd InitMode = "systemd"
)

// HypervisorType is the virtual machine monitor an instance runs on.
type HypervisorType string

const (
	// HypervisorTypeCloudHypervisor is Cloud Hypervisor, the default.
	HypervisorTypeCloudHypervisor HypervisorType = "cloud-hypervisor"

	// HypervisorTypeFirecracker is Firecracker.
	HypervisorTypeFirecracker HypervisorType = "firecracker"

	// DefaultHypervisorType is what an instance that names none runs on.
	DefaultHypervisorType = HypervisorTypeCloudHypervisor
)

// HypervisorTypes returns every hypervisor an instance may run on, the
// default first.
func HypervisorTypes() []HypervisorType {
	return []HypervisorType{HypervisorTypeCloudHypervisor, HypervisorTypeFirecracker}
}

// Valid reports whether t names a hypervisor Dicer can start instances with.
func (t HypervisorType) Valid() bool {
	return slices.Contains(HypervisorTypes(), t)
}

// RestartMode is when an instance is started again without being asked.
type RestartMode string

// The restart modes, as Docker names them.
const (
	// RestartModeNo leaves an instance that ended as it is.
	RestartModeNo RestartMode = "no"

	// RestartModeOnFailure restarts an instance whose end was not clean.
	RestartModeOnFailure RestartMode = "on-failure"

	// RestartModeUnlessStopped restarts an instance however it ended, and
	// starts it when the daemon starts, unless it was last stopped by a
	// user.
	RestartModeUnlessStopped RestartMode = "unless-stopped"

	// RestartModeAlways restarts an instance however it ended, and starts it
	// when the daemon starts, even if it was last stopped by a user.
	RestartModeAlways RestartMode = "always"
)

// RestartPolicy is what the daemon does when an instance ends without being
// asked to. Restarts use exponential backoff.
type RestartPolicy struct {
	Mode RestartMode `yaml:"mode,omitempty" json:"mode,omitempty"`

	// MaxRetries is how many times in a row an on-failure instance is
	// restarted before it is left Failed. Zero means no limit.
	MaxRetries int `yaml:"max_retries,omitempty" json:"max_retries,omitempty"`
}

// Validate returns an invalid argument error if the daemon cannot follow
// the policy.
func (p RestartPolicy) Validate() error {
	switch p.Mode {
	case "", RestartModeNo, RestartModeUnlessStopped, RestartModeAlways:
		if p.MaxRetries != 0 {
			return errdefs.InvalidArgument("a retry limit applies only to the on-failure restart policy")
		}
	case RestartModeOnFailure:
		if p.MaxRetries < 0 {
			return errdefs.InvalidArgument("the retry limit cannot be negative")
		}
	default:
		return errdefs.InvalidArgument("unknown restart policy %q: want no, on-failure[:N], unless-stopped or always", p.Mode)
	}

	return nil
}

// Restarts reports whether the policy ever starts an instance again.
func (p RestartPolicy) Restarts() bool {
	return p.Mode != "" && p.Mode != RestartModeNo
}

// StartsOnBoot reports whether an instance with this policy is started when
// the daemon starts.
func (p RestartPolicy) StartsOnBoot(stoppedByUser bool) bool {
	switch p.Mode {
	case RestartModeAlways:
		return true
	case RestartModeUnlessStopped:
		return !stoppedByUser
	default:
		return false
	}
}

// String is the policy as the CLI and API take it: "on-failure:5".
func (p RestartPolicy) String() string {
	if p.Mode == "" {
		return string(RestartModeNo)
	}
	if p.MaxRetries > 0 {
		return fmt.Sprintf("%s:%d", p.Mode, p.MaxRetries)
	}

	return string(p.Mode)
}
