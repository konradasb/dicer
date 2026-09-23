// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Instance is a virtual machine: what it should be, and what it is.
//
// The two halves are kept apart because they are of different kinds and are
// owned by different things. Spec is desired state -- what a person asked
// for. It is written by create and update, persists across reboots, and
// nothing but a request changes it. Status is observed state -- what the
// host has made of it. It is written by the daemon as the VM starts, runs
// and ends, lives in the runtime directory, and is correctly empty after a
// reboot, because nothing is running then.
//
// Reading one Instance gives both, because a caller almost always wants
// both: "is the VM I asked for running?" is a question about the pair.
type Instance struct {
	Spec   InstanceSpec   `json:"spec"`
	Status InstanceStatus `json:"status"`
}

// InstanceSpec is the persistent definition of a virtual machine: what the
// VM is, not what it is currently doing. Creating an instance records this;
// it does not boot anything.
//
// Referenced resources (kernel, network, volumes, files) are held by name or
// path and resolved at start time, so editing a network or rotating a
// credential file is picked up on the next start rather than being frozen
// into a stale copy.
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

	Ports        []PortMapping     `yaml:"ports,omitempty" json:"ports,omitempty"`
	VolumeMounts []VolumeMount     `yaml:"volume_mounts,omitempty" json:"volume_mounts,omitempty"`
	Files        []FileMount       `yaml:"files,omitempty" json:"files,omitempty"`
	Env          map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Cmd          []string          `yaml:"cmd,omitempty" json:"cmd,omitempty"`

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

	// RemoveOnExit deletes the instance once it stops, whether its guest
	// ended on its own or someone stopped it. The daemon does the deleting,
	// so it happens even if whoever asked for it has gone. An instance its
	// restart policy will start again is not deleted.
	RemoveOnExit bool `yaml:"remove_on_exit,omitempty" json:"remove_on_exit,omitempty"`

	// StoppedByUser records that a user stopped the instance, and has not
	// started it since. It is desired state, kept with the definition so
	// that it survives a host reboot: an unless-stopped instance is not
	// started at boot if it is set.
	StoppedByUser bool `yaml:"stopped_by_user,omitempty" json:"stopped_by_user,omitempty"`
}

// Resources returns what the instance asks for.
func (s InstanceSpec) Resources() Resources {
	return Resources{VCPUs: s.VCPUs, MemoryBytes: s.MemoryBytes}
}

// Hypervisor returns the hypervisor the instance runs on, filling in the
// default for a spec that names none.
func (s InstanceSpec) Hypervisor() HypervisorType {
	if s.HypervisorType == "" {
		return DefaultHypervisor
	}

	return s.HypervisorType
}

// InstanceStatus is the observed state of an instance: what the host has
// made of its spec.
//
// It lives under the runtime directory rather than alongside the spec, so a
// host reboot correctly discards it. Every host resource named here is
// derivable from the instance ID alone, so recovery after an unclean
// shutdown rebuilds this rather than reading a journal.
type InstanceStatus struct {
	InstanceID string        `json:"instance_id,omitempty"`
	State      InstanceState `json:"state"`
	StateError string        `json:"state_error,omitempty"`

	HypervisorPID        *int   `json:"hypervisor_pid,omitempty"`
	HypervisorSocketPath string `json:"hypervisor_socket_path,omitempty"`
	HypervisorVersion    string `json:"hypervisor_version,omitempty"`
	VsockCID             int64  `json:"vsock_cid,omitempty"`
	VsockPath            string `json:"vsock_path,omitempty"`

	// VCPUs and MemoryBytes are what the instance was admitted with, and
	// holds for as long as its state says it holds anything. They are
	// recorded rather than read from the spec, which may have been edited
	// since, and which a restored snapshot's memory need not match.
	VCPUs       int   `json:"vcpus,omitempty"`
	MemoryBytes int64 `json:"memory_bytes,omitempty"`

	// ImageDigest is the image the guest booted from. Its root disk is that
	// image's for as long as it runs, whatever its tag has moved to since.
	ImageDigest string `json:"image_digest,omitempty"`

	// IP and MAC are the instance's address on its network, held for as
	// long as the instance is defined rather than only while it runs.
	IP  string `json:"ip,omitempty"`
	MAC string `json:"mac,omitempty"`

	// HealthCheck is the check this run is monitored with, resolved when it
	// started, or nil for none. It is recorded rather than resolved again,
	// so that a daemon that adopts the VMM checks it the same way.
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

// Held returns what the instance holds of the host's CPU and memory, which
// is nothing unless its state says it holds anything.
func (s InstanceStatus) Held() Resources {
	if !s.State.HoldsResources() {
		return Resources{}
	}

	return Resources{VCPUs: s.VCPUs, MemoryBytes: s.MemoryBytes}
}

// InstanceState is the lifecycle state of an instance.
type InstanceState string

const (
	// StateStopped means the instance is defined but not running. A freshly
	// created instance starts here.
	StateStopped InstanceState = "Stopped"

	// StateStarting means a start is in progress. An instance found in this
	// state at daemon boot crashed mid-start and is cleaned up.
	StateStarting InstanceState = "Starting"

	// StateRunning means the VM is executing.
	StateRunning InstanceState = "Running"

	// StatePaused means the vCPUs are halted but the VM is resident.
	StatePaused InstanceState = "Paused"

	// StateStopping means a shutdown is in progress.
	StateStopping InstanceState = "Stopping"

	// StateRestarting means the instance ended without being asked to, and
	// its restart policy will start it again at
	// InstanceStatus.NextRestartAt. It holds nothing in the meantime.
	StateRestarting InstanceState = "Restarting"

	// StateFailed means the last operation failed; see
	// InstanceStatus.StateError.
	StateFailed InstanceState = "Failed"
)

// InstanceStates returns every lifecycle state, in the order an instance
// normally moves through them.
func InstanceStates() []InstanceState {
	return []InstanceState{
		StateStopped, StateStarting, StateRunning,
		StatePaused, StateStopping, StateRestarting, StateFailed,
	}
}

// validTransitions defines the allowed state transitions.
//
// An instance that ends without being asked to leaves Running or Paused for
// Stopped, Restarting or Failed, as its restart policy decides; a restart
// that fails to start goes back to Restarting, or gives up.
var validTransitions = map[InstanceState][]InstanceState{
	StateStopped:    {StateStarting},
	StateStarting:   {StateRunning, StateRestarting, StateFailed},
	StateRunning:    {StatePaused, StateStopping, StateStopped, StateRestarting, StateFailed},
	StatePaused:     {StateRunning, StateStopping, StateStopped, StateRestarting, StateFailed},
	StateStopping:   {StateStopped, StateFailed},
	StateRestarting: {StateStarting, StateStopping, StateFailed},
	StateFailed:     {StateStarting, StateStopping, StateStopped},
}

// CanTransitionTo reports whether a transition to target is allowed.
func (s InstanceState) CanTransitionTo(target InstanceState) bool {
	return slices.Contains(validTransitions[s], target)
}

// HoldsResources reports whether an instance in the state holds the CPU and
// memory it was admitted with. A start in progress does, so that one
// admitted is counted against the next; so does a paused VM, which is still
// resident.
func (s InstanceState) HoldsResources() bool {
	return s == StateStarting || s.IsActive()
}

// IsActive reports whether the state implies a live hypervisor process.
func (s InstanceState) IsActive() bool {
	return s == StateRunning || s == StatePaused
}

// String returns the state as it is written: "Running".
func (s InstanceState) String() string { return string(s) }

// Lower is the state as a sentence says it: "running", not "Running".
func (s InstanceState) Lower() string { return strings.ToLower(string(s)) }

// InitMode is how the guest starts an instance's command.
type InitMode string

const (
	// ModeAuto has dicer-init decide, once the root filesystem is mounted:
	// systemd if the command is systemd, else exec.
	ModeAuto InitMode = "auto"

	// ModeExec runs the command as PID 1 of its own PID namespace.
	ModeExec InitMode = "exec"

	// ModeSystemd hands the machine's PID 1 to systemd itself.
	ModeSystemd InitMode = "systemd"
)

// ParseInitMode parses a mode as a person gives it. Empty is ModeAuto.
func ParseInitMode(s string) (InitMode, error) {
	switch mode := InitMode(s); mode {
	case "":
		return ModeAuto, nil
	case ModeAuto, ModeExec, ModeSystemd:
		return mode, nil
	default:
		return "", InvalidArgument("unknown init mode %q: want auto, exec or systemd", s)
	}
}

// HypervisorType is the virtual machine monitor an instance runs on.
type HypervisorType string

const (
	// HypervisorCloudHypervisor is Cloud Hypervisor, the default.
	HypervisorCloudHypervisor HypervisorType = "cloud-hypervisor"

	// HypervisorFirecracker is Firecracker.
	HypervisorFirecracker HypervisorType = "firecracker"

	// DefaultHypervisor is what an instance that names none runs on.
	DefaultHypervisor = HypervisorCloudHypervisor
)

// HypervisorTypes returns every hypervisor an instance may run on, the
// default first.
func HypervisorTypes() []HypervisorType {
	return []HypervisorType{HypervisorCloudHypervisor, HypervisorFirecracker}
}

// Valid reports whether t names a hypervisor Dicer can start instances with.
func (t HypervisorType) Valid() bool {
	return slices.Contains(HypervisorTypes(), t)
}

// RestartMode is when an instance is started again without being asked.
type RestartMode string

// The restart modes, as Docker names them.
const (
	// RestartNo leaves an instance that ended as it is.
	RestartNo RestartMode = "no"

	// RestartOnFailure restarts an instance whose end was not clean.
	RestartOnFailure RestartMode = "on-failure"

	// RestartUnlessStopped restarts an instance however it ended, and
	// starts it when the daemon starts, unless it was last stopped by a
	// user.
	RestartUnlessStopped RestartMode = "unless-stopped"

	// RestartAlways restarts an instance however it ended, and starts it
	// when the daemon starts, even if it was last stopped by a user.
	RestartAlways RestartMode = "always"
)

// RestartPolicy is what the daemon does when an instance ends without being
// asked to: when its workload exits, its guest resets or its VMM dies.
//
// A restart waits out an exponential backoff, so that an instance that fails
// as soon as it starts does not take the host with it.
type RestartPolicy struct {
	Mode RestartMode `yaml:"mode,omitempty" json:"mode,omitempty"`

	// MaxRetries is how many times in a row an on-failure instance is
	// restarted before it is left Failed. Zero means no limit.
	MaxRetries int `yaml:"max_retries,omitempty" json:"max_retries,omitempty"`
}

// Validate checks the policy is one the daemon can follow.
func (p RestartPolicy) Validate() error {
	switch p.Mode {
	case "", RestartNo, RestartUnlessStopped, RestartAlways:
		if p.MaxRetries != 0 {
			return InvalidArgument("a retry limit applies only to the on-failure restart policy")
		}
	case RestartOnFailure:
		if p.MaxRetries < 0 {
			return InvalidArgument("the retry limit cannot be negative")
		}
	default:
		return InvalidArgument("unknown restart policy %q: want no, on-failure[:N], unless-stopped or always", p.Mode)
	}

	return nil
}

// Restarts reports whether the policy ever starts an instance again. Only
// "no", which is what an unset policy means, never does.
func (p RestartPolicy) Restarts() bool {
	return p.Mode != "" && p.Mode != RestartNo
}

// StartsOnBoot reports whether an instance with this policy is started when
// the daemon starts. An unless-stopped instance is not, if a user was the one
// who stopped it; an always instance is, either way.
func (p RestartPolicy) StartsOnBoot(stoppedByUser bool) bool {
	switch p.Mode {
	case RestartAlways:
		return true
	case RestartUnlessStopped:
		return !stoppedByUser
	default:
		return false
	}
}

// String is the policy as the CLI and API take it: "on-failure:5".
func (p RestartPolicy) String() string {
	if p.Mode == "" {
		return string(RestartNo)
	}
	if p.MaxRetries > 0 {
		return fmt.Sprintf("%s:%d", p.Mode, p.MaxRetries)
	}

	return string(p.Mode)
}
