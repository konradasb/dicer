// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"cmp"
	"context"
	"log/slog"
	"time"

	"github.com/nrednav/cuid2"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// instanceHandler handles instance-related RPCs.
type instanceHandler struct {
	definitions *filestore.Manager
	instances   *vm.Manager
	defaults    defaultResolver
	logger      *slog.Logger
}

// CreateInstance records an instance definition. It does not boot anything
// unless the request asks for it.
func (h *instanceHandler) CreateInstance(
	ctx context.Context, req *dicerdv1.CreateInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.newInstance(req)
	if err != nil {
		return nil, err
	}

	if err := h.instances.Create(ctx, inst); err != nil {
		return nil, err
	}

	if req.GetStart() {
		if err := h.instances.Start(ctx, inst); err != nil {
			h.logger.ErrorContext(ctx, "instance created but failed to start",
				"instance", inst.Name, "error", err)
			return nil, err
		}
	}

	return h.view(inst)
}

// newInstance validates a create request and returns the instance it
// defines. A kernel or network left out is the daemon's default.
func (h *instanceHandler) newInstance(req *dicerdv1.CreateInstanceRequest) (types.InstanceSpec, error) {
	if err := validateCreate(req); err != nil {
		return types.InstanceSpec{}, err
	}

	var err error
	if req.KernelName, err = h.defaults.resolveKernel(req.GetKernelName()); err != nil {
		return types.InstanceSpec{}, err
	}
	if req.NetworkName, err = h.defaults.resolveNetwork(req.GetNetworkName()); err != nil {
		return types.InstanceSpec{}, err
	}

	if _, err := h.definitions.GetInstance(req.GetName()); err == nil {
		return types.InstanceSpec{}, errdefs.Exists("instance %q already exists", req.GetName())
	}

	imageRef, err := reference.Parse(req.GetImageRef())
	if err != nil {
		return types.InstanceSpec{}, errdefs.InvalidArgument("invalid image %q: %v", req.GetImageRef(), err)
	}
	if err := h.checkCanStart(req); err != nil {
		return types.InstanceSpec{}, err
	}

	ports, err := portMappings(req.GetPorts())
	if err != nil {
		return types.InstanceSpec{}, err
	}
	restart, err := restartPolicyFromProto(req.GetRestartPolicy())
	if err != nil {
		return types.InstanceSpec{}, err
	}
	healthCheck, err := healthCheckFromProto(req.GetHealthCheck())
	if err != nil {
		return types.InstanceSpec{}, err
	}
	initMode, err := initModes.fromProto(req.GetInitMode())
	if err != nil {
		return types.InstanceSpec{}, err
	}
	hypervisorType, err := hypervisorTypes.fromProto(req.GetHypervisorType())
	if err != nil {
		return types.InstanceSpec{}, err
	}
	mounts, err := h.mounts(req.GetMounts())
	if err != nil {
		return types.InstanceSpec{}, err
	}

	now := time.Now()
	inst := types.InstanceSpec{
		ID:                cuid2.Generate(),
		Name:              req.GetName(),
		Hostname:          req.GetHostname(),
		ImageRef:          imageRef.String(),
		HypervisorType:    hypervisorType,
		HypervisorVersion: req.GetHypervisorVersion(),
		KernelName:        req.GetKernelName(),
		KernelArgs:        req.GetKernelArgs(),
		VCPUs:             int(req.GetVcpus()),
		MemoryBytes:       req.GetMemoryBytes(),
		DiskBytes:         req.GetDiskBytes(),
		NetworkName:       req.GetNetworkName(),
		StaticIP:          req.GetStaticIp(),
		Ports:             ports,
		Mounts:            mounts,
		Env:               req.GetEnv(),
		Cmd:               req.GetCmd(),
		Labels:            req.GetLabels(),
		Restart:           restart,
		HealthCheck:       healthCheck,
		InitMode:          cmp.Or(initMode, types.ModeAuto),
		RemoveOnExit:      req.GetRemoveOnExit(),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := checkRemoveOnExit(inst); err != nil {
		return types.InstanceSpec{}, err
	}

	return inst, nil
}

// UpdateInstance modifies an instance's definition. See vm.Manager.Update.
func (h *instanceHandler) UpdateInstance(
	ctx context.Context, req *dicerdv1.UpdateInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, err
	}

	if err := h.applyReferences(&inst, req); err != nil {
		return nil, err
	}
	applySettings(&inst, req)
	if err := h.applyLists(&inst, req); err != nil {
		return nil, err
	}
	if p := req.GetRestartPolicy(); p != nil {
		if inst.Restart, err = restartPolicyFromProto(p); err != nil {
			return nil, err
		}
	}
	if c := req.GetHealthCheck(); c != nil {
		if inst.HealthCheck, err = healthCheckFromProto(c); err != nil {
			return nil, err
		}
	}
	if m := req.GetInitMode(); m != dicerdv1.InitMode_INIT_MODE_UNSPECIFIED {
		if inst.InitMode, err = initModes.fromProto(m); err != nil {
			return nil, err
		}
	}
	if t := req.GetHypervisorType(); t != dicerdv1.HypervisorType_HYPERVISOR_TYPE_UNSPECIFIED {
		if inst.HypervisorType, err = hypervisorTypes.fromProto(t); err != nil {
			return nil, err
		}
	}

	if err := validateResources(int32(inst.VCPUs), inst.MemoryBytes, inst.DiskBytes); err != nil {
		return nil, err
	}
	if err := guest.ValidateHostname(inst.Hostname); err != nil {
		return nil, errdefs.InvalidArgument("%v", err)
	}
	if err := checkRemoveOnExit(inst); err != nil {
		return nil, err
	}
	if err := h.checkStaticIP(inst.NetworkName, inst.StaticIP); err != nil {
		return nil, err
	}
	if err := h.instances.CheckResources(inst.Resources()); err != nil {
		return nil, err
	}

	inst.UpdatedAt = time.Now()
	if err := h.instances.Update(ctx, inst); err != nil {
		return nil, err
	}

	return h.view(inst)
}

// applyReferences applies the image, kernel and network an update names,
// checking that each is valid or exists.
func (h *instanceHandler) applyReferences(inst *types.InstanceSpec, req *dicerdv1.UpdateInstanceRequest) error {
	if v := req.ImageRef; v != nil {
		ref, err := reference.Parse(*v)
		if err != nil {
			return errdefs.InvalidArgument("invalid image %q: %v", *v, err)
		}
		inst.ImageRef = ref.String()
	}
	if v := req.KernelName; v != nil {
		if _, err := h.definitions.GetKernel(*v); err != nil {
			return errdefs.InvalidArgument("%v", err)
		}
		inst.KernelName = *v
	}
	if v := req.NetworkName; v != nil {
		if _, err := h.definitions.GetNetwork(*v); err != nil {
			return errdefs.InvalidArgument("%v", err)
		}
		inst.NetworkName = *v
	}
	return nil
}

// applySettings applies the scalar fields an update sets.
func applySettings(inst *types.InstanceSpec, req *dicerdv1.UpdateInstanceRequest) {
	if v := req.Vcpus; v != nil {
		inst.VCPUs = int(*v)
	}
	setIf(&inst.HypervisorVersion, req.HypervisorVersion)
	setIf(&inst.KernelArgs, req.KernelArgs)
	setIf(&inst.MemoryBytes, req.MemoryBytes)
	setIf(&inst.DiskBytes, req.DiskBytes)
	setIf(&inst.StaticIP, req.StaticIp)
	setIf(&inst.Hostname, req.Hostname)
	setIf(&inst.RemoveOnExit, req.RemoveOnExit)
}

// setIf sets *dst to *v if v is set.
func setIf[T any](dst, v *T) {
	if v != nil {
		*dst = *v
	}
}

// applyLists replaces each list or map an update gives a non-empty value.
func (h *instanceHandler) applyLists(inst *types.InstanceSpec, req *dicerdv1.UpdateInstanceRequest) error {
	if len(req.GetMounts()) > 0 {
		mounts, err := h.mounts(req.GetMounts())
		if err != nil {
			return err
		}
		inst.Mounts = mounts
	}
	if len(req.GetPorts()) > 0 {
		ports, err := portMappings(req.GetPorts())
		if err != nil {
			return err
		}
		inst.Ports = ports
	}
	if len(req.GetEnv()) > 0 {
		inst.Env = req.GetEnv()
	}
	if len(req.GetCmd()) > 0 {
		inst.Cmd = req.GetCmd()
	}
	if len(req.GetLabels()) > 0 {
		inst.Labels = req.GetLabels()
	}
	return nil
}

func (h *instanceHandler) StartInstance(
	ctx context.Context, req *dicerdv1.StartInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, err
	}

	if err := h.instances.Start(ctx, inst); err != nil {
		return nil, err
	}

	return h.view(inst)
}

func (h *instanceHandler) StopInstance(
	ctx context.Context, req *dicerdv1.StopInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, err
	}

	if err := h.instances.Stop(ctx, inst); err != nil {
		return nil, err
	}

	return h.view(inst)
}

func (h *instanceHandler) PauseInstance(
	ctx context.Context, req *dicerdv1.PauseInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, err
	}

	if err := h.instances.Pause(ctx, inst); err != nil {
		return nil, err
	}

	return h.view(inst)
}

func (h *instanceHandler) ResumeInstance(
	ctx context.Context, req *dicerdv1.ResumeInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, err
	}

	if err := h.instances.Resume(ctx, inst); err != nil {
		return nil, err
	}

	return h.view(inst)
}

// RenameInstance changes a stopped instance's name.
func (h *instanceHandler) RenameInstance(
	ctx context.Context, req *dicerdv1.RenameInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, err
	}

	renamed, err := h.instances.Rename(ctx, inst, req.GetNewName())
	if err != nil {
		return nil, err
	}

	return h.view(renamed)
}

func (h *instanceHandler) DeleteInstance(
	ctx context.Context, req *dicerdv1.DeleteInstanceRequest,
) (*emptypb.Empty, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, err
	}

	if err := h.instances.Delete(ctx, inst, req.GetForce()); err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (h *instanceHandler) GetInstance(
	_ context.Context, req *dicerdv1.GetInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, err
	}

	return h.view(inst)
}

func (h *instanceHandler) ListInstances(
	_ context.Context, _ *dicerdv1.ListInstancesRequest,
) (*dicerdv1.ListInstancesResponse, error) {
	instances, err := h.definitions.ListInstances()
	if err != nil {
		return nil, err
	}

	resp := &dicerdv1.ListInstancesResponse{
		Instances: make([]*dicerdv1.Instance, 0, len(instances)),
	}
	for _, inst := range instances {
		view, err := h.view(inst)
		if err != nil {
			return nil, err
		}
		resp.Instances = append(resp.Instances, view)
	}

	return resp, nil
}

// view assembles the API representation of an instance.
func (h *instanceHandler) view(inst types.InstanceSpec) (*dicerdv1.Instance, error) {
	return viewInstance(h.instances, inst)
}

// viewInstance assembles an instance's spec, status, address and health.
func viewInstance(instances *vm.Manager, spec types.InstanceSpec) (*dicerdv1.Instance, error) {
	status, err := instances.Runtime(spec)
	if err != nil {
		return nil, err
	}

	if alloc, err := instances.Address(spec); err == nil {
		status.IP, status.MAC = alloc.IP, alloc.MAC
	}
	if check, health, ok := instances.Health(spec); ok {
		status.HealthCheck, status.Health = &check, &health
	}

	return instanceToProto(types.Instance{Spec: spec, Status: status}), nil
}
