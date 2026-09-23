// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"

	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// CreateInstance defines an instance without starting it. The spec's ID,
// timestamps and status are the daemon's to fill in and are ignored.
//
// A spec that names no kernel or network gets the daemon's default, which
// HostInfo reports.
func (c *Client) CreateInstance(ctx context.Context, spec InstanceSpec) (Instance, error) {
	return c.createInstance(ctx, spec, false)
}

// RunInstance defines an instance and starts it, which is what 'dicer run'
// does. A start that fails leaves the instance defined and stopped, with what
// went wrong in its status.
func (c *Client) RunInstance(ctx context.Context, spec InstanceSpec) (Instance, error) {
	return c.createInstance(ctx, spec, true)
}

func (c *Client) createInstance(ctx context.Context, spec InstanceSpec, start bool) (Instance, error) {
	resp, err := c.daemon.CreateInstance(ctx, &dicerdv1.CreateInstanceRequest{
		Name:              spec.Name,
		ImageRef:          spec.ImageRef,
		HypervisorType:    string(spec.HypervisorType),
		HypervisorVersion: spec.HypervisorVersion,
		KernelName:        spec.KernelName,
		KernelArgs:        spec.KernelArgs,
		Vcpus:             int32(spec.VCPUs),
		MemoryBytes:       spec.MemoryBytes,
		DiskBytes:         spec.DiskBytes,
		NetworkName:       spec.NetworkName,
		StaticIp:          spec.StaticIP,
		Hostname:          spec.Hostname,
		Volumes:           volumeMountsToProto(spec.VolumeMounts),
		Files:             fileMountsToProto(spec.Files),
		Ports:             portMappingsToProto(spec.Ports),
		Env:               spec.Env,
		Cmd:               spec.Cmd,
		Labels:            spec.Labels,
		RestartPolicy:     restartPolicyToProto(spec.Restart),
		HealthCheck:       healthCheckToProto(spec.HealthCheck),
		InitMode:          string(spec.InitMode),
		RemoveOnExit:      spec.RemoveOnExit,
		Start:             start,
	})
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// UpdateInstance changes parts of an instance's spec. What the patch leaves
// unset is left as it is.
//
// Most changes take effect at the instance's next start. A running instance
// accepts a change to its restart policy alone, which the policy is read for
// when it ends; anything else is refused while it runs.
func (c *Client) UpdateInstance(ctx context.Context, patch InstancePatch) (Instance, error) {
	resp, err := c.daemon.UpdateInstance(ctx, patch.toProto())
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// GetInstance returns one instance by name.
func (c *Client) GetInstance(ctx context.Context, name string) (Instance, error) {
	resp, err := c.daemon.GetInstance(ctx, &dicerdv1.GetInstanceRequest{Name: name})
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// ListInstances returns every instance on the host, in name order.
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	resp, err := c.daemon.ListInstances(ctx, &dicerdv1.ListInstancesRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]Instance, 0, len(resp.GetInstances()))
	for _, inst := range resp.GetInstances() {
		out = append(out, instanceFromProto(inst))
	}

	return out, nil
}

// StartInstance boots a defined instance and waits for the guest to be up.
func (c *Client) StartInstance(ctx context.Context, name string) (Instance, error) {
	resp, err := c.daemon.StartInstance(ctx, &dicerdv1.StartInstanceRequest{Name: name})
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// StopInstance shuts an instance down, asking the guest first and killing the
// hypervisor if it does not go.
func (c *Client) StopInstance(ctx context.Context, name string) (Instance, error) {
	resp, err := c.daemon.StopInstance(ctx, &dicerdv1.StopInstanceRequest{Name: name})
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// PauseInstance halts an instance's vCPUs, leaving it resident: it holds its
// memory and its address, and resumes where it left off.
func (c *Client) PauseInstance(ctx context.Context, name string) (Instance, error) {
	resp, err := c.daemon.PauseInstance(ctx, &dicerdv1.PauseInstanceRequest{Name: name})
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// ResumeInstance starts a paused instance's vCPUs again.
func (c *Client) ResumeInstance(ctx context.Context, name string) (Instance, error) {
	resp, err := c.daemon.ResumeInstance(ctx, &dicerdv1.ResumeInstanceRequest{Name: name})
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// RenameInstance changes a stopped instance's name.
//
// The instance keeps its ID, its disks, its snapshots and its address: only
// what people call it changes. A running instance is refused, because its
// name is where its files are kept on the host, and its guest took its
// hostname from the old name when it booted.
func (c *Client) RenameInstance(ctx context.Context, name, newName string) (Instance, error) {
	resp, err := c.daemon.RenameInstance(ctx, &dicerdv1.RenameInstanceRequest{Name: name, NewName: newName})
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// DeleteInstance removes an instance and everything that belongs to it: its
// disks, its snapshots and its address. A running instance is refused unless
// force is set, which stops it first.
func (c *Client) DeleteInstance(ctx context.Context, name string, force bool) error {
	_, err := c.daemon.DeleteInstance(ctx, &dicerdv1.DeleteInstanceRequest{Name: name, Force: force})

	return err
}

// InstancePatch changes parts of an instance's spec. A nil field is left as
// it is; a field set to its zero value is set to that value.
//
// It is a separate type from InstanceSpec because the two answer different
// questions: a spec says what an instance is, and every field of it counts,
// while a patch says what to change, where the difference between "set this
// to nothing" and "leave this alone" is the whole point.
type InstancePatch struct {
	// Name is the instance to change, and is required.
	Name string

	ImageRef          *string
	HypervisorType    *HypervisorType
	HypervisorVersion *string
	KernelName        *string
	KernelArgs        *string
	VCPUs             *int
	MemoryBytes       *int64
	DiskBytes         *int64
	NetworkName       *string
	StaticIP          *string
	Hostname          *string
	InitMode          *InitMode

	// Restart and HealthCheck replace what the instance has when they are
	// set. A HealthCheck with Disabled switches checking off, the image's
	// included.
	Restart     *RestartPolicy
	HealthCheck *HealthCheck

	// RemoveOnExit changes whether the instance is deleted once it stops.
	RemoveOnExit *bool

	// These replace the instance's lists and maps wholesale when they are
	// non-nil. An empty, non-nil value clears what is there.
	VolumeMounts []VolumeMount
	Files        []FileMount
	Ports        []PortMapping
	Env          map[string]string
	Cmd          []string
	Labels       map[string]string
}

func (p InstancePatch) toProto() *dicerdv1.UpdateInstanceRequest {
	req := &dicerdv1.UpdateInstanceRequest{
		Name:              p.Name,
		ImageRef:          p.ImageRef,
		HypervisorVersion: p.HypervisorVersion,
		KernelName:        p.KernelName,
		KernelArgs:        p.KernelArgs,
		MemoryBytes:       p.MemoryBytes,
		DiskBytes:         p.DiskBytes,
		NetworkName:       p.NetworkName,
		StaticIp:          p.StaticIP,
		Hostname:          p.Hostname,
		Volumes:           volumeMountsToProto(p.VolumeMounts),
		Files:             fileMountsToProto(p.Files),
		Ports:             portMappingsToProto(p.Ports),
		Env:               p.Env,
		Cmd:               p.Cmd,
		Labels:            p.Labels,
		HealthCheck:       healthCheckToProto(p.HealthCheck),
		RemoveOnExit:      p.RemoveOnExit,
	}

	if p.HypervisorType != nil {
		req.HypervisorType = ptr(string(*p.HypervisorType))
	}
	if p.InitMode != nil {
		req.InitMode = ptr(string(*p.InitMode))
	}
	if p.VCPUs != nil {
		req.Vcpus = ptr(int32(*p.VCPUs))
	}
	if p.Restart != nil {
		req.RestartPolicy = restartPolicyToProto(*p.Restart)
	}

	return req
}

// ptr returns a pointer to v, for the optional fields of a patch.
func ptr[T any](v T) *T { return &v }
