// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcserver

import (
	"github.com/konradasb/dicer/internal/doctor"
	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/event"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/health"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/instance"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/network"
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

var hypervisorTypes = enum[hypervisor.Type, dicerdv1.HypervisorType]{"hypervisor", map[hypervisor.Type]dicerdv1.HypervisorType{
	hypervisor.TypeCloudHypervisor: dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR,
	hypervisor.TypeFirecracker:     dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER,
}}

var instanceStates = enum[instance.State, dicerdv1.InstanceState]{"instance state", map[instance.State]dicerdv1.InstanceState{
	instance.StateStopped:    dicerdv1.InstanceState_INSTANCE_STATE_STOPPED,
	instance.StateStarting:   dicerdv1.InstanceState_INSTANCE_STATE_STARTING,
	instance.StateRunning:    dicerdv1.InstanceState_INSTANCE_STATE_RUNNING,
	instance.StatePaused:     dicerdv1.InstanceState_INSTANCE_STATE_PAUSED,
	instance.StateStandby:    dicerdv1.InstanceState_INSTANCE_STATE_STANDBY,
	instance.StateStopping:   dicerdv1.InstanceState_INSTANCE_STATE_STOPPING,
	instance.StateRestarting: dicerdv1.InstanceState_INSTANCE_STATE_RESTARTING,
	instance.StateFailed:     dicerdv1.InstanceState_INSTANCE_STATE_FAILED,
}}

var pullPolicies = enum[image.PullPolicy, dicerdv1.PullPolicy]{"pull policy", map[image.PullPolicy]dicerdv1.PullPolicy{
	image.PullPolicyMissing: dicerdv1.PullPolicy_PULL_POLICY_MISSING,
	image.PullPolicyAlways:  dicerdv1.PullPolicy_PULL_POLICY_ALWAYS,
	image.PullPolicyNever:   dicerdv1.PullPolicy_PULL_POLICY_NEVER,
}}

var initModes = enum[guest.InitMode, dicerdv1.InitMode]{"init mode", map[guest.InitMode]dicerdv1.InitMode{
	guest.InitModeAuto:    dicerdv1.InitMode_INIT_MODE_AUTO,
	guest.InitModeExec:    dicerdv1.InitMode_INIT_MODE_EXEC,
	guest.InitModeSystemd: dicerdv1.InitMode_INIT_MODE_SYSTEMD,
}}

var restartModes = enum[instance.RestartMode, dicerdv1.RestartMode]{"restart policy", map[instance.RestartMode]dicerdv1.RestartMode{
	instance.RestartModeNo:            dicerdv1.RestartMode_RESTART_MODE_NO,
	instance.RestartModeOnFailure:     dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE,
	instance.RestartModeUnlessStopped: dicerdv1.RestartMode_RESTART_MODE_UNLESS_STOPPED,
	instance.RestartModeAlways:        dicerdv1.RestartMode_RESTART_MODE_ALWAYS,
}}

var mountTypes = enum[instance.MountType, dicerdv1.MountType]{"mount type", map[instance.MountType]dicerdv1.MountType{
	instance.MountTypeVolume:    dicerdv1.MountType_MOUNT_TYPE_VOLUME,
	instance.MountTypeFile:      dicerdv1.MountType_MOUNT_TYPE_FILE,
	instance.MountTypeTmpfs:     dicerdv1.MountType_MOUNT_TYPE_TMPFS,
	instance.MountTypeDirectory: dicerdv1.MountType_MOUNT_TYPE_DIRECTORY,
}}

var protocols = enum[string, dicerdv1.Protocol]{"protocol", map[string]dicerdv1.Protocol{
	network.ProtocolTCP: dicerdv1.Protocol_PROTOCOL_TCP,
	network.ProtocolUDP: dicerdv1.Protocol_PROTOCOL_UDP,
}}

var healthStatuses = enum[health.Status, dicerdv1.HealthStatus]{"health status", map[health.Status]dicerdv1.HealthStatus{
	health.StatusStarting:  dicerdv1.HealthStatus_HEALTH_STATUS_STARTING,
	health.StatusHealthy:   dicerdv1.HealthStatus_HEALTH_STATUS_HEALTHY,
	health.StatusUnhealthy: dicerdv1.HealthStatus_HEALTH_STATUS_UNHEALTHY,
}}

var architectures = enum[string, dicerdv1.Architecture]{"architecture", map[string]dicerdv1.Architecture{
	kernel.ArchitectureX86_64:  dicerdv1.Architecture_ARCHITECTURE_X86_64,
	kernel.ArchitectureAArch64: dicerdv1.Architecture_ARCHITECTURE_AARCH64,
}}

var snapshotKinds = enum[instance.SnapshotKind, dicerdv1.SnapshotKind]{"snapshot kind", map[instance.SnapshotKind]dicerdv1.SnapshotKind{
	instance.SnapshotKindMemory: dicerdv1.SnapshotKind_SNAPSHOT_KIND_MEMORY,
	instance.SnapshotKindDisk:   dicerdv1.SnapshotKind_SNAPSHOT_KIND_DISK,
}}

var waitConditions = enum[instance.WaitCondition, dicerdv1.WaitCondition]{"wait condition", map[instance.WaitCondition]dicerdv1.WaitCondition{
	instance.WaitConditionStopped: dicerdv1.WaitCondition_WAIT_CONDITION_STOPPED,
	instance.WaitConditionHealthy: dicerdv1.WaitCondition_WAIT_CONDITION_HEALTHY,
}}

var eventKinds = enum[event.Kind, dicerdv1.EventKind]{"event kind", map[event.Kind]dicerdv1.EventKind{
	event.KindInstance: dicerdv1.EventKind_EVENT_KIND_INSTANCE,
	event.KindSnapshot: dicerdv1.EventKind_EVENT_KIND_SNAPSHOT,
	event.KindImage:    dicerdv1.EventKind_EVENT_KIND_IMAGE,
	event.KindNetwork:  dicerdv1.EventKind_EVENT_KIND_NETWORK,
	event.KindVolume:   dicerdv1.EventKind_EVENT_KIND_VOLUME,
	event.KindKernel:   dicerdv1.EventKind_EVENT_KIND_KERNEL,
}}

var eventActions = enum[event.Action, dicerdv1.EventAction]{"event action", map[event.Action]dicerdv1.EventAction{
	event.ActionCreated:          dicerdv1.EventAction_EVENT_ACTION_CREATED,
	event.ActionUpdated:          dicerdv1.EventAction_EVENT_ACTION_UPDATED,
	event.ActionDeleted:          dicerdv1.EventAction_EVENT_ACTION_DELETED,
	event.ActionStarted:          dicerdv1.EventAction_EVENT_ACTION_STARTED,
	event.ActionStopped:          dicerdv1.EventAction_EVENT_ACTION_STOPPED,
	event.ActionPaused:           dicerdv1.EventAction_EVENT_ACTION_PAUSED,
	event.ActionResumed:          dicerdv1.EventAction_EVENT_ACTION_RESUMED,
	event.ActionStandby:          dicerdv1.EventAction_EVENT_ACTION_STANDBY,
	event.ActionExited:           dicerdv1.EventAction_EVENT_ACTION_EXITED,
	event.ActionDied:             dicerdv1.EventAction_EVENT_ACTION_DIED,
	event.ActionRestarting:       dicerdv1.EventAction_EVENT_ACTION_RESTARTING,
	event.ActionRenamed:          dicerdv1.EventAction_EVENT_ACTION_RENAMED,
	event.ActionResized:          dicerdv1.EventAction_EVENT_ACTION_RESIZED,
	event.ActionHealthy:          dicerdv1.EventAction_EVENT_ACTION_HEALTHY,
	event.ActionUnhealthy:        dicerdv1.EventAction_EVENT_ACTION_UNHEALTHY,
	event.ActionSnapshotRestored: dicerdv1.EventAction_EVENT_ACTION_SNAPSHOT_RESTORED,
	event.ActionPulled:           dicerdv1.EventAction_EVENT_ACTION_PULLED,
	event.ActionCollected:        dicerdv1.EventAction_EVENT_ACTION_COLLECTED,
	event.ActionImported:         dicerdv1.EventAction_EVENT_ACTION_IMPORTED,
}}

var logSources = enum[instance.LogSource, dicerdv1.LogSource]{"log source", map[instance.LogSource]dicerdv1.LogSource{
	instance.LogSourceGuest:      dicerdv1.LogSource_LOG_SOURCE_GUEST,
	instance.LogSourceHypervisor: dicerdv1.LogSource_LOG_SOURCE_HYPERVISOR,
}}

var pullStages = enum[image.PullStage, dicerdv1.PullStage]{"pull stage", map[image.PullStage]dicerdv1.PullStage{
	image.PullStageResolving:   dicerdv1.PullStage_PULL_STAGE_RESOLVING,
	image.PullStageDownloading: dicerdv1.PullStage_PULL_STAGE_DOWNLOADING,
	image.PullStageUnpacking:   dicerdv1.PullStage_PULL_STAGE_UNPACKING,
	image.PullStageConverting:  dicerdv1.PullStage_PULL_STAGE_CONVERTING,
}}

var hostCheckStatuses = enum[doctor.Status, dicerdv1.HostCheckStatus]{"host check status", map[doctor.Status]dicerdv1.HostCheckStatus{
	doctor.StatusOK:      dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_OK,
	doctor.StatusWarning: dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_WARNING,
	doctor.StatusFailed:  dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_FAILED,
}}

var hostCheckGroups = enum[doctor.Group, dicerdv1.HostCheckGroup]{"host check group", map[doctor.Group]dicerdv1.HostCheckGroup{
	doctor.GroupHost:      dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_HOST,
	doctor.GroupInstances: dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_INSTANCES,
}}
