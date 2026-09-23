// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/nrednav/cuid2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/guest"
	"github.com/dicer-sh/dicer/internal/image/reference"
	"github.com/dicer-sh/dicer/internal/network"
	"github.com/dicer-sh/dicer/internal/vm"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
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
		return nil, toStatus(err)
	}

	if req.GetStart() {
		if err := h.instances.Start(ctx, inst); err != nil {
			// The definition stays; the caller can inspect the failure and
			// retry the start without redefining the instance.
			h.logger.ErrorContext(ctx, "instance created but failed to start",
				"instance", inst.Name, "error", err)
			return nil, toStatus(err)
		}
	}

	return h.view(inst)
}

// newInstance validates a create request and returns the instance it
// defines. A kernel or network left out is the daemon's default.
func (h *instanceHandler) newInstance(req *dicerdv1.CreateInstanceRequest) (dicer.InstanceSpec, error) {
	if err := validateCreate(req); err != nil {
		return dicer.InstanceSpec{}, err
	}

	var err error
	if req.KernelName, err = h.defaults.resolveKernel(req.GetKernelName()); err != nil {
		return dicer.InstanceSpec{}, err
	}
	if req.NetworkName, err = h.defaults.resolveNetwork(req.GetNetworkName()); err != nil {
		return dicer.InstanceSpec{}, err
	}

	if _, err := h.definitions.GetInstance(req.GetName()); err == nil {
		return dicer.InstanceSpec{}, status.Errorf(codes.AlreadyExists, "instance %q already exists", req.GetName())
	}

	imageRef, err := reference.Parse(req.GetImageRef())
	if err != nil {
		return dicer.InstanceSpec{}, status.Errorf(codes.InvalidArgument, "invalid image %q: %v", req.GetImageRef(), err)
	}
	if err := h.checkCanStart(req); err != nil {
		return dicer.InstanceSpec{}, err
	}

	files, err := fileMounts(req.GetFiles())
	if err != nil {
		return dicer.InstanceSpec{}, err
	}
	ports, err := portMappings(req.GetPorts())
	if err != nil {
		return dicer.InstanceSpec{}, err
	}
	restart, err := restartPolicyFromProto(req.GetRestartPolicy())
	if err != nil {
		return dicer.InstanceSpec{}, err
	}
	healthCheck, err := healthCheckFromProto(req.GetHealthCheck())
	if err != nil {
		return dicer.InstanceSpec{}, err
	}
	initMode, err := initModeFromProto(req.GetInitMode())
	if err != nil {
		return dicer.InstanceSpec{}, err
	}
	mounts, err := h.volumeMounts(req.GetVolumes())
	if err != nil {
		return dicer.InstanceSpec{}, err
	}

	now := time.Now()
	inst := dicer.InstanceSpec{
		ID:                cuid2.Generate(),
		Name:              req.GetName(),
		Hostname:          req.GetHostname(),
		ImageRef:          imageRef.String(),
		HypervisorType:    dicer.HypervisorType(req.GetHypervisorType()),
		HypervisorVersion: req.GetHypervisorVersion(),
		KernelName:        req.GetKernelName(),
		KernelArgs:        req.GetKernelArgs(),
		VCPUs:             int(req.GetVcpus()),
		MemoryBytes:       req.GetMemoryBytes(),
		DiskBytes:         req.GetDiskBytes(),
		NetworkName:       req.GetNetworkName(),
		StaticIP:          req.GetStaticIp(),
		Ports:             ports,
		VolumeMounts:      mounts,
		Files:             files,
		Env:               req.GetEnv(),
		Cmd:               req.GetCmd(),
		Labels:            req.GetLabels(),
		Restart:           restart,
		HealthCheck:       healthCheck,
		InitMode:          initMode,
		RemoveOnExit:      req.GetRemoveOnExit(),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := checkRemoveOnExit(inst); err != nil {
		return dicer.InstanceSpec{}, err
	}

	return inst, nil
}

// checkRemoveOnExit rejects a definition that asks to be deleted when it
// stops and to be started again when it stops. Only one of the two can
// happen, and silently picking one would leave the other quietly ignored.
func checkRemoveOnExit(inst dicer.InstanceSpec) error {
	if inst.RemoveOnExit && inst.Restart.Restarts() {
		return status.Errorf(codes.InvalidArgument,
			"an instance cannot be deleted when it stops and restarted when it stops: "+
				"the restart policy is %s, so drop it or drop the request to delete it", inst.Restart)
	}

	return nil
}

// checkCanStart rejects a definition that could never start: one naming a
// kernel or network that does not exist, or asking for more than this host
// has, however idle. Catching these at creation beats failing at first boot.
func (h *instanceHandler) checkCanStart(req *dicerdv1.CreateInstanceRequest) error {
	if _, err := h.definitions.GetKernel(req.GetKernelName()); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if err := h.checkStaticIP(req.GetNetworkName(), req.GetStaticIp()); err != nil {
		return err
	}
	return toStatus(h.instances.CheckResources(dicer.Resources{
		VCPUs: int(req.GetVcpus()), MemoryBytes: req.GetMemoryBytes(),
	}))
}

// volumeMounts validates the volumes an instance wants attached, defaulting
// the access mode to ReadWriteOnce.
func (h *instanceHandler) volumeMounts(in []*dicerdv1.VolumeMount) ([]dicer.VolumeMount, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > vm.MaxVolumeMounts {
		return nil, status.Errorf(codes.InvalidArgument,
			"%d volumes given: an instance can mount at most %d", len(in), vm.MaxVolumeMounts)
	}

	out := make([]dicer.VolumeMount, 0, len(in))
	volumes := make(map[string]struct{}, len(in))
	paths := make(map[string]struct{}, len(in))
	for _, v := range in {
		name, path := v.GetVolumeName(), v.GetMountPath()
		if _, err := h.definitions.GetVolume(name); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		if !filepath.IsAbs(path) {
			return nil, status.Errorf(codes.InvalidArgument,
				"volume %q: the mount path %q must be absolute, e.g. /data", name, path)
		}
		if path = filepath.Clean(path); path == "/" {
			return nil, status.Errorf(codes.InvalidArgument,
				"volume %q cannot be mounted over the root filesystem", name)
		}

		// A volume attached twice is one disk written through two devices,
		// and two volumes on one path hide one of them.
		if _, dup := volumes[name]; dup {
			return nil, status.Errorf(codes.InvalidArgument, "volume %q is given twice", name)
		}
		volumes[name] = struct{}{}
		if _, dup := paths[path]; dup {
			return nil, status.Errorf(codes.InvalidArgument, "two volumes are mounted at %q", path)
		}
		paths[path] = struct{}{}

		mode := dicer.VolumeAccessMode(v.GetAccessMode())
		switch mode {
		case "":
			mode = dicer.AccessModeReadWriteOnce
		case dicer.AccessModeReadWriteOnce, dicer.AccessModeReadOnlyMany:
		default:
			return nil, status.Errorf(codes.InvalidArgument,
				"invalid access mode %q for volume %q: want %s or %s",
				mode, name, dicer.AccessModeReadWriteOnce, dicer.AccessModeReadOnlyMany)
		}

		out = append(out, dicer.VolumeMount{
			VolumeName: name,
			MountPath:  path,
			AccessMode: mode,
		})
	}

	return out, nil
}

// fileMounts validates the host files an instance wants injected.
//
// The path is checked at definition time so a typo surfaces now rather than
// at first boot, but the contents are deliberately not read or copied here:
// the file is read at every start, so rotating it on the host is picked up
// without redefining the instance.
func fileMounts(in []*dicerdv1.FileMount) ([]dicer.FileMount, error) {
	if len(in) == 0 {
		return nil, nil
	}

	out := make([]dicer.FileMount, 0, len(in))
	seen := make(map[string]struct{}, len(in))

	for _, f := range in {
		name, hostPath := f.GetName(), f.GetHostPath()

		switch {
		case name == "":
			return nil, status.Error(codes.InvalidArgument, "a file needs a name")
		case name != filepath.Base(name) || name == "." || name == "..":
			return nil, status.Errorf(codes.InvalidArgument,
				"invalid file name %q: it becomes a filename under /run/secrets", name)
		case hostPath == "":
			return nil, status.Errorf(codes.InvalidArgument, "file %q needs a host path", name)
		case !filepath.IsAbs(hostPath):
			return nil, status.Errorf(codes.InvalidArgument,
				"file %q: the host path %q must be absolute", name, hostPath)
		}

		if _, dup := seen[name]; dup {
			return nil, status.Errorf(codes.InvalidArgument, "file %q is given twice", name)
		}
		seen[name] = struct{}{}

		info, err := os.Stat(hostPath)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument,
				"file %q: cannot read %q: %v", name, hostPath, err)
		}
		if info.IsDir() {
			return nil, status.Errorf(codes.InvalidArgument,
				"file %q: %q is a directory", name, hostPath)
		}

		out = append(out, dicer.FileMount{Name: name, HostPath: hostPath})
	}

	return out, nil
}

// portMappings validates the ports an instance wants published. Whether the
// host ports are free is only known at start, so it is checked then.
func portMappings(in []*dicerdv1.PortMapping) ([]dicer.PortMapping, error) {
	if len(in) == 0 {
		return nil, nil
	}

	out := make([]dicer.PortMapping, 0, len(in))
	for _, p := range in {
		// Checked here because the conversion to uint16 would wrap.
		if p.GetHostPort() > 65535 || p.GetGuestPort() > 65535 {
			return nil, status.Errorf(codes.InvalidArgument,
				"port %d:%d: ports must be between 1 and 65535", p.GetHostPort(), p.GetGuestPort())
		}

		protocol := p.GetProtocol()
		if protocol == "" {
			protocol = dicer.ProtocolTCP
		}

		out = append(out, dicer.PortMapping{
			HostIP:    canonicalHostIP(p.GetHostIp()),
			HostPort:  uint16(p.GetHostPort()),
			GuestPort: uint16(p.GetGuestPort()),
			Protocol:  protocol,
		})
	}

	if err := dicer.ValidatePorts(out); err != nil {
		return nil, toStatus(err)
	}

	return out, nil
}

// canonicalHostIP writes an IPv4 address the way the host rules match it,
// and 0.0.0.0 as every address, as Docker does: a rule for 0.0.0.0 itself
// would match no traffic at all. Anything else is left for ValidatePorts to
// refuse.
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

func validateCreate(req *dicerdv1.CreateInstanceRequest) error {
	if err := dicer.ValidateName(req.GetName()); err != nil {
		return toStatus(err)
	}

	if req.GetImageRef() == "" {
		return status.Error(codes.InvalidArgument, "an instance needs an image")
	}
	if err := guest.ValidateHostname(req.GetHostname()); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return validateResources(req.GetVcpus(), req.GetMemoryBytes(), req.GetDiskBytes())
}

// validateResources checks the sizing an instance is created or updated with.
func validateResources(vcpus int32, memoryBytes, diskBytes int64) error {
	switch {
	case vcpus <= 0:
		return status.Error(codes.InvalidArgument, "an instance needs at least 1 vCPU")
	case memoryBytes <= 0:
		return status.Error(codes.InvalidArgument, "an instance needs more than 0 bytes of memory")
	case diskBytes <= 0:
		return status.Error(codes.InvalidArgument, "an instance needs a disk of more than 0 bytes")
	}
	return nil
}

// UpdateInstance modifies a stopped instance's definition. Editing a running
// instance is refused: the change could not take effect until a restart, and
// silently diverging the definition from what is running is worse than saying
// no.
func (h *instanceHandler) UpdateInstance(
	ctx context.Context, req *dicerdv1.UpdateInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, toStatus(err)
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
	if req.InitMode != nil {
		if inst.InitMode, err = initModeFromProto(req.GetInitMode()); err != nil {
			return nil, err
		}
	}

	if err := validateResources(int32(inst.VCPUs), inst.MemoryBytes, inst.DiskBytes); err != nil {
		return nil, err
	}
	if err := guest.ValidateHostname(inst.Hostname); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := checkRemoveOnExit(inst); err != nil {
		return nil, err
	}
	if err := h.checkStaticIP(inst.NetworkName, inst.StaticIP); err != nil {
		return nil, err
	}
	if err := h.instances.CheckResources(inst.Resources()); err != nil {
		return nil, toStatus(err)
	}

	inst.UpdatedAt = time.Now()
	if err := h.instances.Update(ctx, inst); err != nil {
		return nil, toStatus(err)
	}

	return h.view(inst)
}

// applyReferences applies the image, kernel and network an update names,
// checking that each is valid or exists.
func (h *instanceHandler) applyReferences(inst *dicer.InstanceSpec, req *dicerdv1.UpdateInstanceRequest) error {
	if v := req.ImageRef; v != nil {
		ref, err := reference.Parse(*v)
		if err != nil {
			return status.Errorf(codes.InvalidArgument, "invalid image %q: %v", *v, err)
		}
		inst.ImageRef = ref.String()
	}
	if v := req.KernelName; v != nil {
		if _, err := h.definitions.GetKernel(*v); err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		inst.KernelName = *v
	}
	if v := req.NetworkName; v != nil {
		if _, err := h.definitions.GetNetwork(*v); err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		inst.NetworkName = *v
	}
	return nil
}

// applySettings applies the scalar fields an update sets.
func applySettings(inst *dicer.InstanceSpec, req *dicerdv1.UpdateInstanceRequest) {
	if v := req.HypervisorType; v != nil {
		inst.HypervisorType = dicer.HypervisorType(*v)
	}
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

// initModeFromProto checks an init mode. Empty is auto.
func initModeFromProto(s string) (dicer.InitMode, error) {
	mode, err := dicer.ParseInitMode(s)
	if err != nil {
		return "", status.Error(codes.InvalidArgument, err.Error())
	}
	return mode, nil
}

// restartPolicyFromProto converts and checks a restart policy. None at all
// is the default, no.
func restartPolicyFromProto(p *dicerdv1.RestartPolicy) (dicer.RestartPolicy, error) {
	policy := dicer.RestartPolicy{
		Mode:       dicer.RestartMode(p.GetMode()),
		MaxRetries: int(p.GetMaxRetries()),
	}
	if policy.Mode == "" {
		policy.Mode = dicer.RestartNo
	}
	if err := policy.Validate(); err != nil {
		return dicer.RestartPolicy{}, status.Error(codes.InvalidArgument, err.Error())
	}
	return policy, nil
}

// checkStaticIP checks that a network exists and, if ip is set, that it is an
// address the network can give an instance: in its subnet, and neither a
// reserved address nor its gateway. Whether another instance holds it is only
// known at start.
func (h *instanceHandler) checkStaticIP(networkName, ip string) error {
	nw, err := h.definitions.GetNetwork(networkName)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if ip == "" {
		return nil
	}

	ipNet, err := network.ParseSubnet(nw.Subnet)
	if err != nil {
		return toStatus(err)
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || !network.Assignable(ipNet, parsed) || parsed.Equal(net.ParseIP(nw.Gateway)) {
		return status.Errorf(codes.InvalidArgument,
			"static IP %q is not an address network %q can assign: its subnet is %s, and its gateway %s",
			ip, nw.Name, nw.Subnet, nw.Gateway)
	}
	return nil
}

// setIf sets *dst to *v if v is set.
func setIf[T any](dst, v *T) {
	if v != nil {
		*dst = *v
	}
}

// applyLists replaces each list or map an update gives a non-empty value.
func (h *instanceHandler) applyLists(inst *dicer.InstanceSpec, req *dicerdv1.UpdateInstanceRequest) error {
	if len(req.GetVolumes()) > 0 {
		mounts, err := h.volumeMounts(req.GetVolumes())
		if err != nil {
			return err
		}
		inst.VolumeMounts = mounts
	}
	if len(req.GetFiles()) > 0 {
		files, err := fileMounts(req.GetFiles())
		if err != nil {
			return err
		}
		inst.Files = files
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
		return nil, toStatus(err)
	}

	if err := h.instances.Start(ctx, inst); err != nil {
		return nil, toStatus(err)
	}

	return h.view(inst)
}

func (h *instanceHandler) StopInstance(
	ctx context.Context, req *dicerdv1.StopInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	if err := h.instances.Stop(ctx, inst); err != nil {
		return nil, toStatus(err)
	}

	return h.view(inst)
}

func (h *instanceHandler) PauseInstance(
	ctx context.Context, req *dicerdv1.PauseInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	if err := h.instances.Pause(ctx, inst); err != nil {
		return nil, toStatus(err)
	}

	return h.view(inst)
}

func (h *instanceHandler) ResumeInstance(
	ctx context.Context, req *dicerdv1.ResumeInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	if err := h.instances.Resume(ctx, inst); err != nil {
		return nil, toStatus(err)
	}

	return h.view(inst)
}

// RenameInstance changes a stopped instance's name.
func (h *instanceHandler) RenameInstance(
	ctx context.Context, req *dicerdv1.RenameInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	renamed, err := h.instances.Rename(ctx, inst, req.GetNewName())
	if err != nil {
		return nil, toStatus(err)
	}

	return h.view(renamed)
}

func (h *instanceHandler) DeleteInstance(
	ctx context.Context, req *dicerdv1.DeleteInstanceRequest,
) (*emptypb.Empty, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	if err := h.instances.Delete(ctx, inst, req.GetForce()); err != nil {
		return nil, toStatus(err)
	}

	return &emptypb.Empty{}, nil
}

func (h *instanceHandler) GetInstance(
	_ context.Context, req *dicerdv1.GetInstanceRequest,
) (*dicerdv1.Instance, error) {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	return h.view(inst)
}

func (h *instanceHandler) ListInstances(
	_ context.Context, _ *dicerdv1.ListInstancesRequest,
) (*dicerdv1.ListInstancesResponse, error) {
	instances, err := h.definitions.ListInstances()
	if err != nil {
		return nil, toStatus(err)
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
func (h *instanceHandler) view(inst dicer.InstanceSpec) (*dicerdv1.Instance, error) {
	return viewInstance(h.instances, inst)
}

// viewInstance assembles what the API says an instance is: its spec, plus
// whatever status, address and health it currently has.
//
// The address is joined onto the status here because it is held by the
// network manager rather than by the instance: an instance keeps its address
// while it is defined, not only while it runs.
func viewInstance(instances *vm.Manager, spec dicer.InstanceSpec) (*dicerdv1.Instance, error) {
	status, err := instances.Runtime(spec)
	if err != nil {
		return nil, toStatus(err)
	}

	if alloc, err := instances.Address(spec); err == nil {
		status.IP, status.MAC = alloc.IP, alloc.MAC
	}
	if check, health, ok := instances.Health(spec); ok {
		status.HealthCheck, status.Health = &check, &health
	}

	return instanceToProto(dicer.Instance{Spec: spec, Status: status}), nil
}
