// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"cmp"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/network"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// instanceToProto flattens an instance's spec and status into the single view
// the API presents. Status fields are left zero when the instance is not
// running.
//
// The client reads this message back into the same dicer.Instance; see
// instanceFromProto in the root package, which is the other half of this.
func instanceToProto(instance dicer.Instance) *dicerdv1.Instance {
	inst, rt := instance.Spec, instance.Status

	out := &dicerdv1.Instance{
		Id:                inst.ID,
		Name:              inst.Name,
		Hostname:          inst.Hostname,
		ImageRef:          inst.ImageRef,
		HypervisorType:    string(inst.HypervisorType),
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
		InitMode:          string(cmp.Or(inst.InitMode, dicer.ModeAuto)),
		CreateTime:        timestamppb.New(inst.CreatedAt),
		UpdateTime:        timestamppb.New(inst.UpdatedAt),

		State:        string(rt.State),
		StateError:   rt.StateError,
		VsockCid:     rt.VsockCID,
		RestartCount: int32(rt.RestartCount),
	}

	for _, m := range inst.VolumeMounts {
		out.Volumes = append(out.Volumes, &dicerdv1.VolumeMount{
			VolumeName: m.VolumeName,
			MountPath:  m.MountPath,
			AccessMode: string(m.AccessMode),
		})
	}

	for _, f := range inst.Files {
		out.Files = append(out.Files, &dicerdv1.FileMount{Name: f.Name, HostPath: f.HostPath})
	}

	for _, p := range inst.Ports {
		out.Ports = append(out.Ports, &dicerdv1.PortMapping{
			HostIp:    p.HostIP,
			HostPort:  uint32(p.HostPort),
			GuestPort: uint32(p.GuestPort),
			Protocol:  p.Proto(),
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

func restartPolicyToProto(p dicer.RestartPolicy) *dicerdv1.RestartPolicy {
	mode := p.Mode
	if mode == "" {
		mode = dicer.RestartNo
	}
	return &dicerdv1.RestartPolicy{Mode: string(mode), MaxRetries: int32(p.MaxRetries)}
}

// networkToProto converts a network, reporting address usage from the
// allocation count rather than storing it on the network itself.
func networkToProto(n dicer.Network, allocated int) *dicerdv1.Network {
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

// allocationToProto converts an allocation, deriving the TAP device name
// rather than reading a stored copy of it.
func allocationToProto(a dicer.NetworkAllocation, instanceName string) *dicerdv1.NetworkAllocation {
	return &dicerdv1.NetworkAllocation{
		InstanceId:   a.InstanceID,
		InstanceName: instanceName,
		Ip:           a.IP,
		Mac:          a.MAC,
		TapDevice:    network.TAPName(a.InstanceID),
	}
}

// snapshotToProto converts a snapshot, naming the instance it belongs to
// rather than exposing its ID.
func snapshotToProto(snap dicer.Snapshot, instanceName string) *dicerdv1.Snapshot {
	return &dicerdv1.Snapshot{
		Name:              snap.Name,
		InstanceName:      instanceName,
		HypervisorType:    string(snap.HypervisorType),
		HypervisorVersion: snap.HypervisorVersion,
		MemoryBytes:       snap.MemoryBytes,
		SizeBytes:         snap.SizeBytes,
		CreateTime:        timestamppb.New(snap.CreatedAt),
	}
}

func volumeToProto(v dicer.Volume) *dicerdv1.Volume {
	return &dicerdv1.Volume{
		Id:         v.ID,
		Name:       v.Name,
		SizeBytes:  v.SizeBytes,
		CreateTime: timestamppb.New(v.CreatedAt),
		UpdateTime: timestamppb.New(v.UpdatedAt),
	}
}

func kernelToProto(k dicer.Kernel) *dicerdv1.Kernel {
	return &dicerdv1.Kernel{
		Id:         k.ID,
		Name:       k.Name,
		Arch:       k.Arch,
		Url:        k.URL,
		Sha256:     k.SHA256,
		CreateTime: timestamppb.New(k.CreatedAt),
		UpdateTime: timestamppb.New(k.UpdatedAt),
	}
}

func imageToProto(img *dicer.Image) *dicerdv1.Image {
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

func clientToProto(c dicer.TrustedClient) *dicerdv1.Client {
	out := &dicerdv1.Client{
		Name:        c.Name,
		Fingerprint: c.Fingerprint,
		CreateTime:  timestamppb.New(c.CreatedAt),
		Certificate: c.Certificate,
	}

	// What the certificate says is shown if it can be read; that it cannot
	// is no reason to hide the client, whose trust rests on the fingerprint.
	if cert, err := c.ParseCertificate(); err == nil {
		out.Subject = cert.Subject.String()
		out.ExpireTime = timestamppb.New(cert.NotAfter)
	}

	return out
}

func tokenToProto(t dicer.AccessToken) *dicerdv1.Token {
	return &dicerdv1.Token{
		Name:       t.Name,
		CreateTime: timestamppb.New(t.CreatedAt),
		ExpireTime: timestamppb.New(t.ExpiresAt),
	}
}
