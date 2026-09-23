// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// instanceFromProto is InstanceToProto backwards.
func instanceFromProto(p *dicerdv1.Instance) Instance {
	if p == nil {
		return Instance{}
	}

	inst := Instance{
		Spec: InstanceSpec{
			ID:                p.GetId(),
			Name:              p.GetName(),
			Hostname:          p.GetHostname(),
			ImageRef:          p.GetImageRef(),
			HypervisorType:    HypervisorType(p.GetHypervisorType()),
			HypervisorVersion: p.GetHypervisorVersion(),
			KernelName:        p.GetKernelName(),
			KernelArgs:        p.GetKernelArgs(),
			VCPUs:             int(p.GetVcpus()),
			MemoryBytes:       p.GetMemoryBytes(),
			DiskBytes:         p.GetDiskBytes(),
			NetworkName:       p.GetNetworkName(),
			StaticIP:          p.GetStaticIp(),
			Env:               p.GetEnv(),
			Cmd:               p.GetCmd(),
			Labels:            p.GetLabels(),
			Restart:           restartPolicyFromProto(p.GetRestartPolicy()),
			HealthCheck:       healthCheckFromProto(p.GetHealthCheck()),
			InitMode:          InitMode(p.GetInitMode()),
			RemoveOnExit:      p.GetRemoveOnExit(),
			CreatedAt:         goTime(p.GetCreateTime()),
			UpdatedAt:         goTime(p.GetUpdateTime()),
		},
		Status: InstanceStatus{
			InstanceID: p.GetId(),
			State:      InstanceState(p.GetState()),
			StateError: p.GetStateError(),

			// The API carries one hypervisor version: the running VMM's if
			// there is one, else what the spec pinned. Which it was is not
			// on the wire, so it is reported as both, and a caller that
			// wants the effective one reads either.
			HypervisorVersion: p.GetHypervisorVersion(),
			VsockCID:          p.GetVsockCid(),
			IP:                p.GetIp(),
			MAC:               p.GetMac(),
			RestartCount:      int(p.GetRestartCount()),
			StartedAt:         goTime(p.GetStartTime()),
			FinishedAt:        goTime(p.GetFinishTime()),
			NextRestartAt:     goTime(p.GetNextRestartTime()),
		},
	}

	for _, m := range p.GetVolumes() {
		inst.Spec.VolumeMounts = append(inst.Spec.VolumeMounts, VolumeMount{
			VolumeName: m.GetVolumeName(),
			MountPath:  m.GetMountPath(),
			AccessMode: VolumeAccessMode(m.GetAccessMode()),
		})
	}

	for _, f := range p.GetFiles() {
		inst.Spec.Files = append(inst.Spec.Files, FileMount{Name: f.GetName(), HostPath: f.GetHostPath()})
	}

	for _, m := range p.GetPorts() {
		inst.Spec.Ports = append(inst.Spec.Ports, PortMapping{
			HostIP:    m.GetHostIp(),
			HostPort:  uint16(m.GetHostPort()),
			GuestPort: uint16(m.GetGuestPort()),
			Protocol:  m.GetProtocol(),
		})
	}

	if pid := p.GetHypervisorPid(); pid != 0 {
		n := int(pid)
		inst.Status.HypervisorPID = &n
	}
	if p.ExitCode != nil {
		code := int(p.GetExitCode())
		inst.Status.ExitCode = &code
	}
	if h := p.GetHealth(); h != nil {
		check, health := healthFromProto(h)
		inst.Status.HealthCheck, inst.Status.Health = check, &health
	}

	return inst
}

// restartPolicyToProto converts a restart policy, naming the default rather
// than leaving it to be inferred from an empty string.

// restartPolicyToProto converts a restart policy. A policy that was never set
// is left out altogether rather than sent as "no": the two mean the same
// thing to the daemon, and saying nothing is what a request that did not
// mention it should say.
func restartPolicyToProto(p RestartPolicy) *dicerdv1.RestartPolicy {
	if p == (RestartPolicy{}) {
		return nil
	}

	return &dicerdv1.RestartPolicy{Mode: string(p.Mode), MaxRetries: int32(p.MaxRetries)}
}

// restartPolicyFromProto is restartPolicyToProto backwards.

// restartPolicyFromProto is restartPolicyToProto backwards.
func restartPolicyFromProto(p *dicerdv1.RestartPolicy) RestartPolicy {
	if p == nil {
		return RestartPolicy{}
	}

	return RestartPolicy{
		Mode:       RestartMode(p.GetMode()),
		MaxRetries: int(p.GetMaxRetries()),
	}
}

// portMappingsToProto converts an instance's published ports.

// portMappingsToProto converts an instance's published ports.
// portMappingsToProto converts the ports of a request. The protocol is sent
// as it was given, empty included: what an unset one means is the daemon's to
// decide, and a client that fills it in here would be deciding for it.
func portMappingsToProto(ports []PortMapping) []*dicerdv1.PortMapping {
	if ports == nil {
		return nil
	}

	out := make([]*dicerdv1.PortMapping, 0, len(ports))
	for _, p := range ports {
		out = append(out, &dicerdv1.PortMapping{
			HostIp:    p.HostIP,
			HostPort:  uint32(p.HostPort),
			GuestPort: uint32(p.GuestPort),
			Protocol:  p.Protocol,
		})
	}

	return out
}

// volumeMountsToProto is volumeMountsFromProto backwards.

// volumeMountsToProto is volumeMountsFromProto backwards.
func volumeMountsToProto(mounts []VolumeMount) []*dicerdv1.VolumeMount {
	if mounts == nil {
		return nil
	}

	out := make([]*dicerdv1.VolumeMount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, &dicerdv1.VolumeMount{
			VolumeName: m.VolumeName,
			MountPath:  m.MountPath,
			AccessMode: string(m.AccessMode),
		})
	}

	return out
}

// fileMountsToProto is fileMountsFromProto backwards.

// fileMountsToProto is fileMountsFromProto backwards.
func fileMountsToProto(files []FileMount) []*dicerdv1.FileMount {
	if files == nil {
		return nil
	}

	out := make([]*dicerdv1.FileMount, 0, len(files))
	for _, f := range files {
		out = append(out, &dicerdv1.FileMount{Name: f.Name, HostPath: f.HostPath})
	}

	return out
}
