// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"cmp"
	"fmt"
	"net"
	"os"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// validateCreate checks the fields of a create request that need no lookup.
func validateCreate(req *dicerdv1.CreateInstanceRequest) error {
	if err := naming.Validate(req.GetName()); err != nil {
		return err
	}

	if req.GetImageRef() == "" {
		return errdefs.InvalidArgument("an instance needs an image")
	}
	if err := guest.ValidateHostname(req.GetHostname()); err != nil {
		return errdefs.InvalidArgument("%v", err)
	}
	return validateResources(req.GetVcpus(), req.GetMemoryBytes(), req.GetDiskBytes())
}

// validateResources checks the sizing an instance is created or updated with.
func validateResources(vcpus int32, memoryBytes, diskBytes int64) error {
	switch {
	case vcpus <= 0:
		return errdefs.InvalidArgument("an instance needs at least 1 vCPU")
	case memoryBytes <= 0:
		return errdefs.InvalidArgument("an instance needs more than 0 bytes of memory")
	case diskBytes <= 0:
		return errdefs.InvalidArgument("an instance needs a disk of more than 0 bytes")
	}
	return nil
}

// validateMaximums checks the most an instance can be resized to while it
// runs: none is below what it asks for, and Firecracker, which cannot add
// vCPUs to a running guest, has no vCPU maximum.
func validateMaximums(instance types.InstanceSpec) error {
	switch {
	case instance.MaxVCPUs < 0 || instance.MaxVCPUs > 0 && instance.MaxVCPUs < instance.VCPUs:
		return errdefs.InvalidArgument("max_vcpus %d is below the instance's %s",
			instance.MaxVCPUs, humanize.Count(instance.VCPUs, "vCPU"))
	case instance.MaxMemoryBytes < 0 || instance.MaxMemoryBytes > 0 && instance.MaxMemoryBytes < instance.MemoryBytes:
		return errdefs.InvalidArgument("max_memory_bytes %s is below the instance's %s memory",
			humanize.Bytes(instance.MaxMemoryBytes), humanize.Bytes(instance.MemoryBytes))
	case instance.MaxVCPUs > 0 && instance.EffectiveHypervisorType() == types.HypervisorTypeFirecracker:
		return errdefs.InvalidArgument("firecracker cannot add vCPUs to a running guest: leave max_vcpus unset, " +
			"or use cloud-hypervisor")
	}
	return nil
}

// checkRemoveOnExit rejects remove-on-exit combined with a restart policy.
func checkRemoveOnExit(instance types.InstanceSpec) error {
	if instance.RemoveOnExit && instance.Restart.Restarts() {
		return errdefs.InvalidArgument(
			"an instance cannot be deleted when it stops and restarted when it stops: "+
				"the restart policy is %s, so drop it or drop the request to delete it", instance.Restart)
	}

	return nil
}

// checkCanStart rejects a definition that could never start: a missing
// kernel or network, or more resources than the host allows.
func (h *instanceHandler) checkCanStart(req *dicerdv1.CreateInstanceRequest) error {
	if _, err := h.definitions.Kernel(req.GetKernelName()); err != nil {
		return errdefs.InvalidArgument("%v", err)
	}
	if err := h.checkStaticIP(req.GetNetworkName(), req.GetStaticIp()); err != nil {
		return err
	}
	return h.instances.CheckResources(types.Resources{
		VCPUs:       int(max(req.GetVcpus(), req.GetMaxVcpus())),
		MemoryBytes: max(req.GetMemoryBytes(), req.GetMaxMemoryBytes()),
	})
}

// checkStaticIP checks that a network exists and that ip, if set, is an
// assignable address on it.
func (h *instanceHandler) checkStaticIP(networkName, ip string) error {
	n, err := h.definitions.Network(networkName)
	if err != nil {
		return errdefs.InvalidArgument("%v", err)
	}
	if ip == "" {
		return nil
	}

	subnet, err := network.ParseSubnet(n.Subnet)
	if err != nil {
		return err
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || !network.Assignable(subnet, parsed) || parsed.Equal(net.ParseIP(n.Gateway)) {
		return errdefs.InvalidArgument(
			"static IP %q is not an address network %q can assign: its subnet is %s, and its gateway %s",
			ip, n.Name, n.Subnet, n.Gateway)
	}
	return nil
}

// mounts validates what an instance wants mounted: the volumes must exist,
// and the host files must exist but are read only at start.
func (h *instanceHandler) mounts(in []*dicerdv1.Mount) ([]types.Mount, error) {
	if len(in) == 0 {
		return nil, nil
	}

	mounts := make([]types.Mount, 0, len(in))
	for _, m := range in {
		mountType, err := mountTypes.fromProto(m.GetType())
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, types.Mount{
			Type:     mountType,
			Source:   m.GetSource(),
			Target:   m.GetTarget(),
			ReadOnly: m.GetReadOnly(),
		})
	}

	mounts, err := types.ValidateMounts(mounts)
	if err != nil {
		return nil, err
	}

	volumes := 0
	for _, m := range mounts {
		switch m.Type {
		case types.MountTypeVolume:
			volumes++
			if _, err := h.definitions.Volume(m.Source); err != nil {
				return nil, errdefs.InvalidArgument("%v", err)
			}
		case types.MountTypeFile:
			if err := checkHostFile(m.Source); err != nil {
				return nil, errdefs.InvalidArgument("mount on %s: %v", m.Target, err)
			}
		}
	}
	if volumes > vm.MaxVolumeMounts {
		return nil, errdefs.InvalidArgument(
			"%d volumes given: an instance can mount at most %d", volumes, vm.MaxVolumeMounts)
	}

	return mounts, nil
}

// checkHostFile checks that path is a file the daemon can read.
func checkHostFile(path string) error {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return fmt.Errorf("cannot read %q: %w", path, err)
	case info.IsDir():
		return fmt.Errorf("%q is a directory: only single files can be mounted from the host", path)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%q is not a regular file", path)
	}
	return nil
}

// portMappings validates the ports an instance wants published. Clashes with
// other instances are checked at start.
func portMappings(in []*dicerdv1.PortMapping) ([]types.PortMapping, error) {
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

	if err := types.ValidatePorts(out); err != nil {
		return nil, err
	}

	return out, nil
}

// canonicalHostIP normalises an IPv4 address, turning 0.0.0.0 into "" (every
// address). Anything else is left for ValidatePorts.
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
