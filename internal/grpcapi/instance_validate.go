// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"fmt"
	"net"
	"os"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
)

// checkCanStart rejects a definition that could never start: a missing
// kernel, network or volume, a host file the daemon cannot read, more
// volumes than a guest can have, or more resources than the host allows.
func (h *instanceHandler) checkCanStart(instance types.InstanceSpec) error {
	if _, err := h.definitions.Kernel(instance.KernelName); err != nil {
		return errdefs.InvalidArgument("%v", err)
	}
	if err := h.checkStaticIP(instance.NetworkName, instance.StaticIP); err != nil {
		return err
	}
	if err := h.checkMounts(instance.Mounts); err != nil {
		return err
	}
	return h.instances.CheckResources(instance.MaxResources())
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

// checkMounts checks what of an instance's mounts needs the host: the
// volumes must exist, and fit the guest's disks, and the host files must
// exist, though they are read only at start.
func (h *instanceHandler) checkMounts(mounts []types.Mount) error {
	volumes := 0
	for _, m := range mounts {
		switch m.Type {
		case types.MountTypeVolume:
			volumes++
			if _, err := h.definitions.Volume(m.Source); err != nil {
				return errdefs.InvalidArgument("%v", err)
			}
		case types.MountTypeFile:
			if err := checkHostFile(m.Source); err != nil {
				return errdefs.InvalidArgument("mount on %s: %v", m.Target, err)
			}
		}
	}
	if volumes > vm.MaxVolumeMounts {
		return errdefs.InvalidArgument(
			"%d volumes given: an instance can mount at most %d", volumes, vm.MaxVolumeMounts)
	}
	return nil
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
