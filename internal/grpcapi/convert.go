// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"cmp"
	"net"
	"path"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// instanceToProto flattens an instance's spec and status into one message.
func instanceToProto(instance types.Instance) *dicerdv1.Instance {
	spec, status := instance.Spec, instance.Status

	out := &dicerdv1.Instance{
		Id:                     spec.ID,
		Name:                   spec.Name,
		Hostname:               spec.Hostname,
		ImageRef:               spec.ImageRef,
		HypervisorType:         hypervisorTypes.toProto(spec.EffectiveHypervisorType()),
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
		StandbyAfter:           optionalDuration(spec.StandbyAfter),
		NetworkName:            spec.NetworkName,
		StaticIp:               spec.StaticIP,
		Env:                    spec.Env,
		Cmd:                    spec.Cmd,
		Labels:                 spec.Labels,
		RestartPolicy:          restartPolicyToProto(spec.Restart),
		HealthCheck:            healthCheckToProto(spec.HealthCheck),
		InitMode:               initModes.toProto(cmp.Or(spec.InitMode, types.InitModeAuto)),
		CreateTime:             timestamppb.New(spec.CreatedAt),
		UpdateTime:             timestamppb.New(spec.UpdatedAt),

		State:        instanceStates.toProto(status.State),
		StateError:   status.StateError,
		VsockCid:     status.VsockCID,
		RestartCount: int32(status.RestartCount),
	}

	for _, m := range spec.Mounts {
		out.Mounts = append(out.Mounts, &dicerdv1.Mount{
			Type:     mountTypes.toProto(m.Type),
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	for _, p := range spec.Ports {
		out.Ports = append(out.Ports, &dicerdv1.PortMapping{
			HostIp:    p.HostIP,
			HostPort:  uint32(p.HostPort),
			GuestPort: uint32(p.GuestPort),
			Protocol:  protocols.toProto(p.EffectiveProtocol()),
		})
	}

	if status.VMMPID != nil {
		out.HypervisorPid = int64(*status.VMMPID)
	}
	if !status.StartedAt.IsZero() {
		out.StartTime = timestamppb.New(status.StartedAt)
	}
	if status.HypervisorVersion != "" {
		out.HypervisorVersion = status.HypervisorVersion
	}
	out.Ip, out.Mac = status.IP, status.MAC
	if status.HealthCheck != nil && status.Health != nil {
		out.Health = healthToProto(*status.HealthCheck, *status.Health)
	}
	if status.ExitCode != nil {
		code := int32(*status.ExitCode)
		out.ExitCode = &code
	}
	if !status.FinishedAt.IsZero() {
		out.FinishTime = timestamppb.New(status.FinishedAt)
	}
	if !status.NextRestartAt.IsZero() {
		out.NextRestartTime = timestamppb.New(status.NextRestartAt)
	}

	return out
}

// restartPolicyToProto converts a restart policy; unset is "no".
func restartPolicyToProto(p types.RestartPolicy) *dicerdv1.RestartPolicy {
	mode := p.Mode
	if mode == "" {
		mode = types.RestartModeNo
	}
	return &dicerdv1.RestartPolicy{Mode: restartModes.toProto(mode), MaxRetries: int32(p.MaxRetries)}
}

// networkToProto converts a network with its address usage.
func networkToProto(n types.Network, allocated int) *dicerdv1.Network {
	total, free := n.IPCounts(allocated)

	return &dicerdv1.Network{
		Id:          n.ID,
		Name:        n.Name,
		Subnet:      n.Subnet,
		Gateway:     n.Gateway,
		Bridge:      n.Bridge,
		Mtu:         int32(n.MTU),
		Nameservers: n.Nameservers,
		Isolated:    n.Isolated,
		Internal:    n.Internal,
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

// snapshotToProto converts a snapshot of the instance now named
// instanceName.
func snapshotToProto(snapshot types.Snapshot, instanceName string) *dicerdv1.Snapshot {
	return &dicerdv1.Snapshot{
		Id:                snapshot.ID,
		Name:              snapshot.Name,
		Kind:              snapshotKinds.toProto(snapshot.Kind),
		InstanceId:        snapshot.Instance.ID,
		InstanceName:      instanceName,
		HypervisorType:    hypervisorTypes.toProto(snapshot.HypervisorType),
		HypervisorVersion: snapshot.HypervisorVersion,
		Vcpus:             int32(snapshot.VCPUs),
		MemoryBytes:       snapshot.MemoryBytes,
		SizeBytes:         snapshot.SizeBytes,
		CreateTime:        timestamppb.New(snapshot.CreatedAt),
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
		Arch:       architectures.toProto(k.Architecture),
		Sha256:     k.SHA256,
		CreateTime: timestamppb.New(k.CreatedAt),
		UpdateTime: timestamppb.New(k.UpdatedAt),
	}
}

func imageToProto(image *types.Image) *dicerdv1.Image {
	out := &dicerdv1.Image{
		Name:        image.Name,
		Digest:      image.Digest,
		SizeBytes:   image.SizeBytes,
		CreateTime:  timestamppb.New(image.CreatedAt),
		UpdateTime:  timestamppb.New(image.UpdatedAt),
		HealthCheck: healthCheckToProto(image.HealthCheck),
	}
	if !image.LastUsedAt.IsZero() {
		out.LastUsedTime = timestamppb.New(image.LastUsedAt)
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
		policy.Mode = types.RestartModeNo
	}
	if err := policy.Validate(); err != nil {
		return types.RestartPolicy{}, errdefs.InvalidArgument("%v", err)
	}
	return policy, nil
}

// mountsFromProto converts what an instance wants mounted, cleaning the
// targets. InstanceSpec.Validate checks them, and checkMounts what needs the
// host.
func mountsFromProto(in []*dicerdv1.Mount) ([]types.Mount, error) {
	if len(in) == 0 {
		return nil, nil
	}

	mounts := make([]types.Mount, 0, len(in))
	for _, m := range in {
		mountType, err := mountTypes.fromProto(m.GetType())
		if err != nil {
			return nil, err
		}
		target := m.GetTarget()
		// Cleaning nothing would make it ".", which Validate would call
		// relative rather than missing.
		if target != "" {
			target = path.Clean(target)
		}
		mounts = append(mounts, types.Mount{
			Type:     mountType,
			Source:   m.GetSource(),
			Target:   target,
			ReadOnly: m.GetReadOnly(),
		})
	}
	return mounts, nil
}

// portMappingsFromProto converts the ports an instance wants published.
// InstanceSpec.Validate checks them, and their clashes with other instances
// are checked at start.
func portMappingsFromProto(in []*dicerdv1.PortMapping) ([]types.PortMapping, error) {
	if len(in) == 0 {
		return nil, nil
	}

	out := make([]types.PortMapping, 0, len(in))
	for _, p := range in {
		if p.GetHostPort() > 65535 || p.GetGuestPort() > 65535 {
			return nil, errdefs.InvalidArgument(
				"port %d:%d: ports must be between 1 and 65535", p.GetHostPort(), p.GetGuestPort())
		}

		protocol, err := protocols.fromProto(p.GetProtocol())
		if err != nil {
			return nil, err
		}

		out = append(out, types.PortMapping{
			HostIP:    canonicalHostIP(p.GetHostIp()),
			HostPort:  uint16(p.GetHostPort()),
			GuestPort: uint16(p.GetGuestPort()),
			Protocol:  cmp.Or(protocol, types.ProtocolTCP),
		})
	}

	return out, nil
}

// canonicalHostIP normalises an IPv4 address, turning 0.0.0.0 into "" (every
// address). Anything else is left for InstanceSpec.Validate.
func canonicalHostIP(s string) string {
	ip := net.ParseIP(s).To4()
	switch {
	case ip == nil:
		return s
	case ip.IsUnspecified():
		return ""
	default:
		return ip.String()
	}
}
