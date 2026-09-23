// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"github.com/konradasb/dicer/internal/errdefs"
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
	types.HypervisorCloudHypervisor: dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR,
	types.HypervisorFirecracker:     dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER,
}}

var instanceStates = enum[types.InstanceState, dicerdv1.InstanceState]{"instance state", map[types.InstanceState]dicerdv1.InstanceState{
	types.StateStopped:    dicerdv1.InstanceState_INSTANCE_STATE_STOPPED,
	types.StateStarting:   dicerdv1.InstanceState_INSTANCE_STATE_STARTING,
	types.StateRunning:    dicerdv1.InstanceState_INSTANCE_STATE_RUNNING,
	types.StatePaused:     dicerdv1.InstanceState_INSTANCE_STATE_PAUSED,
	types.StateStopping:   dicerdv1.InstanceState_INSTANCE_STATE_STOPPING,
	types.StateRestarting: dicerdv1.InstanceState_INSTANCE_STATE_RESTARTING,
	types.StateFailed:     dicerdv1.InstanceState_INSTANCE_STATE_FAILED,
}}

var initModes = enum[types.InitMode, dicerdv1.InitMode]{"init mode", map[types.InitMode]dicerdv1.InitMode{
	types.ModeAuto:    dicerdv1.InitMode_INIT_MODE_AUTO,
	types.ModeExec:    dicerdv1.InitMode_INIT_MODE_EXEC,
	types.ModeSystemd: dicerdv1.InitMode_INIT_MODE_SYSTEMD,
}}

var restartModes = enum[types.RestartMode, dicerdv1.RestartMode]{"restart policy", map[types.RestartMode]dicerdv1.RestartMode{
	types.RestartNo:            dicerdv1.RestartMode_RESTART_MODE_NO,
	types.RestartOnFailure:     dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE,
	types.RestartUnlessStopped: dicerdv1.RestartMode_RESTART_MODE_UNLESS_STOPPED,
	types.RestartAlways:        dicerdv1.RestartMode_RESTART_MODE_ALWAYS,
}}

var mountTypes = enum[types.MountType, dicerdv1.MountType]{"mount type", map[types.MountType]dicerdv1.MountType{
	types.MountVolume: dicerdv1.MountType_MOUNT_TYPE_VOLUME,
	types.MountFile:   dicerdv1.MountType_MOUNT_TYPE_FILE,
	types.MountTmpfs:  dicerdv1.MountType_MOUNT_TYPE_TMPFS,
}}

var protocols = enum[string, dicerdv1.Protocol]{"protocol", map[string]dicerdv1.Protocol{
	types.ProtocolTCP: dicerdv1.Protocol_PROTOCOL_TCP,
	types.ProtocolUDP: dicerdv1.Protocol_PROTOCOL_UDP,
}}

var healthStatuses = enum[types.HealthStatus, dicerdv1.HealthStatus]{"health status", map[types.HealthStatus]dicerdv1.HealthStatus{
	types.HealthStarting:  dicerdv1.HealthStatus_HEALTH_STATUS_STARTING,
	types.HealthHealthy:   dicerdv1.HealthStatus_HEALTH_STATUS_HEALTHY,
	types.HealthUnhealthy: dicerdv1.HealthStatus_HEALTH_STATUS_UNHEALTHY,
}}

var architectures = enum[string, dicerdv1.Architecture]{"architecture", map[string]dicerdv1.Architecture{
	types.ArchX86_64:  dicerdv1.Architecture_ARCHITECTURE_X86_64,
	types.ArchAArch64: dicerdv1.Architecture_ARCHITECTURE_AARCH64,
}}

var eventKinds = enum[types.EventKind, dicerdv1.EventKind]{"event kind", map[types.EventKind]dicerdv1.EventKind{
	types.KindInstance: dicerdv1.EventKind_EVENT_KIND_INSTANCE,
	types.KindImage:    dicerdv1.EventKind_EVENT_KIND_IMAGE,
}}

var eventActions = enum[types.EventAction, dicerdv1.EventAction]{"event action", map[types.EventAction]dicerdv1.EventAction{
	types.ActionCreated:          dicerdv1.EventAction_EVENT_ACTION_CREATED,
	types.ActionUpdated:          dicerdv1.EventAction_EVENT_ACTION_UPDATED,
	types.ActionDeleted:          dicerdv1.EventAction_EVENT_ACTION_DELETED,
	types.ActionStarted:          dicerdv1.EventAction_EVENT_ACTION_STARTED,
	types.ActionStopped:          dicerdv1.EventAction_EVENT_ACTION_STOPPED,
	types.ActionPaused:           dicerdv1.EventAction_EVENT_ACTION_PAUSED,
	types.ActionResumed:          dicerdv1.EventAction_EVENT_ACTION_RESUMED,
	types.ActionExited:           dicerdv1.EventAction_EVENT_ACTION_EXITED,
	types.ActionDied:             dicerdv1.EventAction_EVENT_ACTION_DIED,
	types.ActionRestarting:       dicerdv1.EventAction_EVENT_ACTION_RESTARTING,
	types.ActionRenamed:          dicerdv1.EventAction_EVENT_ACTION_RENAMED,
	types.ActionHealthy:          dicerdv1.EventAction_EVENT_ACTION_HEALTHY,
	types.ActionUnhealthy:        dicerdv1.EventAction_EVENT_ACTION_UNHEALTHY,
	types.ActionSnapshotCreated:  dicerdv1.EventAction_EVENT_ACTION_SNAPSHOT_CREATED,
	types.ActionSnapshotRestored: dicerdv1.EventAction_EVENT_ACTION_SNAPSHOT_RESTORED,
	types.ActionSnapshotDeleted:  dicerdv1.EventAction_EVENT_ACTION_SNAPSHOT_DELETED,
	types.ActionPulled:           dicerdv1.EventAction_EVENT_ACTION_PULLED,
	types.ActionCollected:        dicerdv1.EventAction_EVENT_ACTION_COLLECTED,
}}

var logSources = enum[vm.LogSource, dicerdv1.LogSource]{"log source", map[vm.LogSource]dicerdv1.LogSource{
	vm.LogSourceGuest:      dicerdv1.LogSource_LOG_SOURCE_GUEST,
	vm.LogSourceHypervisor: dicerdv1.LogSource_LOG_SOURCE_HYPERVISOR,
}}

var pullStages = enum[types.PullStage, dicerdv1.PullStage]{"pull stage", map[types.PullStage]dicerdv1.PullStage{
	types.StageResolving:   dicerdv1.PullStage_PULL_STAGE_RESOLVING,
	types.StageDownloading: dicerdv1.PullStage_PULL_STAGE_DOWNLOADING,
	types.StageUnpacking:   dicerdv1.PullStage_PULL_STAGE_UNPACKING,
	types.StageConverting:  dicerdv1.PullStage_PULL_STAGE_CONVERTING,
}}
