// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"cmp"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// instanceToProto flattens an instance's spec and status into one message.
func instanceToProto(instance types.Instance) *dicerdv1.Instance {
	inst, rt := instance.Spec, instance.Status

	out := &dicerdv1.Instance{
		Id:                inst.ID,
		Name:              inst.Name,
		Hostname:          inst.Hostname,
		ImageRef:          inst.ImageRef,
		HypervisorType:    hypervisorTypes.toProto(inst.HypervisorType),
		HypervisorVersion: inst.HypervisorVersion,
		KernelName:        inst.KernelName,
		KernelArgs:        inst.KernelArgs,
		Vcpus:             int32(inst.VCPUs),
		MemoryBytes:       inst.MemoryBytes,
		DiskBytes:         inst.DiskBytes,
		NetworkName:       inst.NetworkName,
		StaticIp:          inst.StaticIP,
		Env:               inst.Env,
		Cmd:               inst.Cmd,
		Labels:            inst.Labels,
		RestartPolicy:     restartPolicyToProto(inst.Restart),
		HealthCheck:       healthCheckToProto(inst.HealthCheck),
		InitMode:          initModes.toProto(cmp.Or(inst.InitMode, types.ModeAuto)),
		CreateTime:        timestamppb.New(inst.CreatedAt),
		UpdateTime:        timestamppb.New(inst.UpdatedAt),

		State:        instanceStates.toProto(rt.State),
		StateError:   rt.StateError,
		VsockCid:     rt.VsockCID,
		RestartCount: int32(rt.RestartCount),
	}

	for _, m := range inst.Mounts {
		out.Mounts = append(out.Mounts, &dicerdv1.Mount{
			Type:     mountTypes.toProto(m.Type),
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	for _, p := range inst.Ports {
		out.Ports = append(out.Ports, &dicerdv1.PortMapping{
			HostIp:    p.HostIP,
			HostPort:  uint32(p.HostPort),
			GuestPort: uint32(p.GuestPort),
			Protocol:  protocols.toProto(p.Proto()),
		})
	}

	if rt.HypervisorPID != nil {
		out.HypervisorPid = int64(*rt.HypervisorPID)
	}
	if !rt.StartedAt.IsZero() {
		out.StartTime = timestamppb.New(rt.StartedAt)
	}
	if rt.HypervisorVersion != "" {
		out.HypervisorVersion = rt.HypervisorVersion
	}
	out.Ip, out.Mac = rt.IP, rt.MAC
	if rt.HealthCheck != nil && rt.Health != nil {
		out.Health = healthToProto(*rt.HealthCheck, *rt.Health)
	}
	if rt.ExitCode != nil {
		code := int32(*rt.ExitCode)
		out.ExitCode = &code
	}
	if !rt.FinishedAt.IsZero() {
		out.FinishTime = timestamppb.New(rt.FinishedAt)
	}
	if !rt.NextRestartAt.IsZero() {
		out.NextRestartTime = timestamppb.New(rt.NextRestartAt)
	}

	return out
}

// restartPolicyToProto converts a restart policy; unset is "no".
func restartPolicyToProto(p types.RestartPolicy) *dicerdv1.RestartPolicy {
	mode := p.Mode
	if mode == "" {
		mode = types.RestartNo
	}
	return &dicerdv1.RestartPolicy{Mode: restartModes.toProto(mode), MaxRetries: int32(p.MaxRetries)}
}

// networkToProto converts a network with its address usage.
func networkToProto(n types.Network, allocated int) *dicerdv1.Network {
	total, free := n.Usage(allocated)

	return &dicerdv1.Network{
		Id:          n.ID,
		Name:        n.Name,
		Subnet:      n.Subnet,
		Gateway:     n.Gateway,
		Bridge:      n.Bridge,
		Mtu:         int32(n.MTU),
		Nameservers: n.Nameservers,
		Isolated:    n.Isolated,
		TotalIps:    total,
		FreeIps:     free,
		CreateTime:  timestamppb.New(n.CreatedAt),
		UpdateTime:  timestamppb.New(n.UpdatedAt),
	}
}

// allocationToProto converts an allocation, deriving its TAP device name.
func allocationToProto(a types.NetworkAllocation, instanceName string) *dicerdv1.NetworkAllocation {
	return &dicerdv1.NetworkAllocation{
		InstanceId:   a.InstanceID,
		InstanceName: instanceName,
		Ip:           a.IP,
		Mac:          a.MAC,
		TapDevice:    network.TAPName(a.InstanceID),
	}
}

// snapshotToProto converts a snapshot of the named instance.
func snapshotToProto(snap types.Snapshot, instanceName string) *dicerdv1.Snapshot {
	return &dicerdv1.Snapshot{
		Name:              snap.Name,
		InstanceName:      instanceName,
		HypervisorType:    hypervisorTypes.toProto(snap.HypervisorType),
		HypervisorVersion: snap.HypervisorVersion,
		MemoryBytes:       snap.MemoryBytes,
		SizeBytes:         snap.SizeBytes,
		CreateTime:        timestamppb.New(snap.CreatedAt),
	}
}

func volumeToProto(v types.Volume) *dicerdv1.Volume {
	return &dicerdv1.Volume{
		Id:         v.ID,
		Name:       v.Name,
		SizeBytes:  v.SizeBytes,
		CreateTime: timestamppb.New(v.CreatedAt),
		UpdateTime: timestamppb.New(v.UpdatedAt),
	}
}

func kernelToProto(k types.Kernel) *dicerdv1.Kernel {
	return &dicerdv1.Kernel{
		Id:         k.ID,
		Name:       k.Name,
		Arch:       architectures.toProto(k.Arch),
		Url:        k.URL,
		Sha256:     k.SHA256,
		CreateTime: timestamppb.New(k.CreatedAt),
		UpdateTime: timestamppb.New(k.UpdatedAt),
	}
}

func imageToProto(img *types.Image) *dicerdv1.Image {
	out := &dicerdv1.Image{
		Name:        img.Name,
		Digest:      img.Digest,
		SizeBytes:   img.SizeBytes,
		CreateTime:  timestamppb.New(img.CreatedAt),
		UpdateTime:  timestamppb.New(img.UpdatedAt),
		HealthCheck: healthCheckToProto(img.HealthCheck),
	}
	if !img.LastUsedAt.IsZero() {
		out.LastUsedTime = timestamppb.New(img.LastUsedAt)
	}
	return out
}

// restartPolicyFromProto converts and validates a restart policy. Unset is
// "no".
func restartPolicyFromProto(p *dicerdv1.RestartPolicy) (types.RestartPolicy, error) {
	mode, err := restartModes.fromProto(p.GetMode())
	if err != nil {
		return types.RestartPolicy{}, err
	}
	policy := types.RestartPolicy{Mode: mode, MaxRetries: int(p.GetMaxRetries())}
	if policy.Mode == "" {
		policy.Mode = types.RestartNo
	}
	if err := policy.Validate(); err != nil {
		return types.RestartPolicy{}, errdefs.InvalidArgument("%v", err)
	}
	return policy, nil
}
