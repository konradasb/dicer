// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// enum pairs the values of one of the daemon's enumerations with the API's.
type enum[T comparable, P ~int32] struct {
	// what names the enumeration, as an error does: "hypervisor".
	what   string
	values map[T]P
}

// toProto returns the API's value for v: unspecified for the zero value, or
// for one the API has none for.
func (e enum[T, P]) toProto(v T) P {
	return e.values[v]
}

// fromProto returns the daemon's value for p: the zero value for
// unspecified, and an error for one the daemon does not know.
func (e enum[T, P]) fromProto(p P) (T, error) {
	var zero T
	if p == 0 {
		return zero, nil
	}

	for v, candidate := range e.values {
		if candidate == p {
			return v, nil
		}
	}

	return zero, errdefs.InvalidArgument("unknown %s %v", e.what, p)
}

var hypervisorTypes = enum[types.HypervisorType, dicerdv1.HypervisorType]{"hypervisor", map[types.HypervisorType]dicerdv1.HypervisorType{
	types.HypervisorTypeCloudHypervisor: dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR,
	types.HypervisorTypeFirecracker:     dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER,
}}

var instanceStates = enum[types.InstanceState, dicerdv1.InstanceState]{"instance state", map[types.InstanceState]dicerdv1.InstanceState{
	types.InstanceStateStopped:    dicerdv1.InstanceState_INSTANCE_STATE_STOPPED,
	types.InstanceStateStarting:   dicerdv1.InstanceState_INSTANCE_STATE_STARTING,
	types.InstanceStateRunning:    dicerdv1.InstanceState_INSTANCE_STATE_RUNNING,
	types.InstanceStatePaused:     dicerdv1.InstanceState_INSTANCE_STATE_PAUSED,
	types.InstanceStateStopping:   dicerdv1.InstanceState_INSTANCE_STATE_STOPPING,
	types.InstanceStateRestarting: dicerdv1.InstanceState_INSTANCE_STATE_RESTARTING,
	types.InstanceStateFailed:     dicerdv1.InstanceState_INSTANCE_STATE_FAILED,
}}

var pullPolicies = enum[types.PullPolicy, dicerdv1.PullPolicy]{"pull policy", map[types.PullPolicy]dicerdv1.PullPolicy{
	types.PullPolicyMissing: dicerdv1.PullPolicy_PULL_POLICY_MISSING,
	types.PullPolicyAlways:  dicerdv1.PullPolicy_PULL_POLICY_ALWAYS,
	types.PullPolicyNever:   dicerdv1.PullPolicy_PULL_POLICY_NEVER,
}}

var initModes = enum[types.InitMode, dicerdv1.InitMode]{"init mode", map[types.InitMode]dicerdv1.InitMode{
	types.InitModeAuto:    dicerdv1.InitMode_INIT_MODE_AUTO,
	types.InitModeExec:    dicerdv1.InitMode_INIT_MODE_EXEC,
	types.InitModeSystemd: dicerdv1.InitMode_INIT_MODE_SYSTEMD,
}}

var restartModes = enum[types.RestartMode, dicerdv1.RestartMode]{"restart policy", map[types.RestartMode]dicerdv1.RestartMode{
	types.RestartModeNo:            dicerdv1.RestartMode_RESTART_MODE_NO,
	types.RestartModeOnFailure:     dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE,
	types.RestartModeUnlessStopped: dicerdv1.RestartMode_RESTART_MODE_UNLESS_STOPPED,
	types.RestartModeAlways:        dicerdv1.RestartMode_RESTART_MODE_ALWAYS,
}}

var mountTypes = enum[types.MountType, dicerdv1.MountType]{"mount type", map[types.MountType]dicerdv1.MountType{
	types.MountTypeVolume: dicerdv1.MountType_MOUNT_TYPE_VOLUME,
	types.MountTypeFile:   dicerdv1.MountType_MOUNT_TYPE_FILE,
	types.MountTypeTmpfs:  dicerdv1.MountType_MOUNT_TYPE_TMPFS,
}}

var protocols = enum[string, dicerdv1.Protocol]{"protocol", map[string]dicerdv1.Protocol{
	types.ProtocolTCP: dicerdv1.Protocol_PROTOCOL_TCP,
	types.ProtocolUDP: dicerdv1.Protocol_PROTOCOL_UDP,
}}

var healthStatuses = enum[types.HealthStatus, dicerdv1.HealthStatus]{"health status", map[types.HealthStatus]dicerdv1.HealthStatus{
	types.HealthStatusStarting:  dicerdv1.HealthStatus_HEALTH_STATUS_STARTING,
	types.HealthStatusHealthy:   dicerdv1.HealthStatus_HEALTH_STATUS_HEALTHY,
	types.HealthStatusUnhealthy: dicerdv1.HealthStatus_HEALTH_STATUS_UNHEALTHY,
}}

var architectures = enum[string, dicerdv1.Architecture]{"architecture", map[string]dicerdv1.Architecture{
	types.ArchitectureX86_64:  dicerdv1.Architecture_ARCHITECTURE_X86_64,
	types.ArchitectureAArch64: dicerdv1.Architecture_ARCHITECTURE_AARCH64,
}}

var snapshotKinds = enum[types.SnapshotKind, dicerdv1.SnapshotKind]{"snapshot kind", map[types.SnapshotKind]dicerdv1.SnapshotKind{
	types.SnapshotKindMemory: dicerdv1.SnapshotKind_SNAPSHOT_KIND_MEMORY,
	types.SnapshotKindDisk:   dicerdv1.SnapshotKind_SNAPSHOT_KIND_DISK,
}}

var eventKinds = enum[events.Kind, dicerdv1.EventKind]{"event kind", map[events.Kind]dicerdv1.EventKind{
	events.KindInstance: dicerdv1.EventKind_EVENT_KIND_INSTANCE,
	events.KindSnapshot: dicerdv1.EventKind_EVENT_KIND_SNAPSHOT,
	events.KindImage:    dicerdv1.EventKind_EVENT_KIND_IMAGE,
	events.KindNetwork:  dicerdv1.EventKind_EVENT_KIND_NETWORK,
	events.KindVolume:   dicerdv1.EventKind_EVENT_KIND_VOLUME,
	events.KindKernel:   dicerdv1.EventKind_EVENT_KIND_KERNEL,
}}

var eventActions = enum[events.Action, dicerdv1.EventAction]{"event action", map[events.Action]dicerdv1.EventAction{
	events.ActionCreated:          dicerdv1.EventAction_EVENT_ACTION_CREATED,
	events.ActionUpdated:          dicerdv1.EventAction_EVENT_ACTION_UPDATED,
	events.ActionDeleted:          dicerdv1.EventAction_EVENT_ACTION_DELETED,
	events.ActionStarted:          dicerdv1.EventAction_EVENT_ACTION_STARTED,
	events.ActionStopped:          dicerdv1.EventAction_EVENT_ACTION_STOPPED,
	events.ActionPaused:           dicerdv1.EventAction_EVENT_ACTION_PAUSED,
	events.ActionResumed:          dicerdv1.EventAction_EVENT_ACTION_RESUMED,
	events.ActionExited:           dicerdv1.EventAction_EVENT_ACTION_EXITED,
	events.ActionDied:             dicerdv1.EventAction_EVENT_ACTION_DIED,
	events.ActionRestarting:       dicerdv1.EventAction_EVENT_ACTION_RESTARTING,
	events.ActionRenamed:          dicerdv1.EventAction_EVENT_ACTION_RENAMED,
	events.ActionResized:          dicerdv1.EventAction_EVENT_ACTION_RESIZED,
	events.ActionHealthy:          dicerdv1.EventAction_EVENT_ACTION_HEALTHY,
	events.ActionUnhealthy:        dicerdv1.EventAction_EVENT_ACTION_UNHEALTHY,
	events.ActionSnapshotRestored: dicerdv1.EventAction_EVENT_ACTION_SNAPSHOT_RESTORED,
	events.ActionPulled:           dicerdv1.EventAction_EVENT_ACTION_PULLED,
	events.ActionCollected:        dicerdv1.EventAction_EVENT_ACTION_COLLECTED,
	events.ActionImported:         dicerdv1.EventAction_EVENT_ACTION_IMPORTED,
	events.ActionFetched:          dicerdv1.EventAction_EVENT_ACTION_FETCHED,
}}

var logSources = enum[vm.LogSource, dicerdv1.LogSource]{"log source", map[vm.LogSource]dicerdv1.LogSource{
	vm.LogSourceGuest:      dicerdv1.LogSource_LOG_SOURCE_GUEST,
	vm.LogSourceHypervisor: dicerdv1.LogSource_LOG_SOURCE_HYPERVISOR,
}}

var pullStages = enum[types.PullStage, dicerdv1.PullStage]{"pull stage", map[types.PullStage]dicerdv1.PullStage{
	types.PullStageResolving:   dicerdv1.PullStage_PULL_STAGE_RESOLVING,
	types.PullStageDownloading: dicerdv1.PullStage_PULL_STAGE_DOWNLOADING,
	types.PullStageUnpacking:   dicerdv1.PullStage_PULL_STAGE_UNPACKING,
	types.PullStageConverting:  dicerdv1.PullStage_PULL_STAGE_CONVERTING,
}}
