// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer/internal/filestore"
)

// Defaults names what an instance gets when its definition leaves a
// reference out. Either may be empty.
type Defaults struct {
	Kernel  string
	Network string
}

// defaultResolver works out the kernel and network an instance gets when it
// names none: the configured default, or else the only one there is -- so
// that on a host with one of each, 'dicer run nginx' needs no flags at all.
type defaultResolver struct {
	definitions *filestore.Manager
	configured  Defaults
}

// kernel returns the default kernel, or "" if there is none.
func (r defaultResolver) kernel() string {
	if r.configured.Kernel != "" {
		return r.configured.Kernel
	}

	kernels, err := r.definitions.ListKernels()
	if err != nil || len(kernels) != 1 {
		return ""
	}
	return kernels[0].Name
}

// network returns the default network, or "" if there is none.
func (r defaultResolver) network() string {
	if r.configured.Network != "" {
		return r.configured.Network
	}

	networks, err := r.definitions.ListNetworks()
	if err != nil || len(networks) != 1 {
		return ""
	}
	return networks[0].Name
}

// resolveKernel returns name, or the default kernel if it is empty, or an
// error that says how to get one.
func (r defaultResolver) resolveKernel(name string) (string, error) {
	if name != "" {
		return name, nil
	}
	if name = r.kernel(); name != "" {
		return name, nil
	}

	kernels, err := r.definitions.ListKernels()
	if err != nil {
		return "", toStatus(err)
	}
	names := make([]string, 0, len(kernels))
	for _, k := range kernels {
		names = append(names, k.Name)
	}

	return "", noDefaultError("kernel", names)
}

// resolveNetwork returns name, or the default network if it is empty, or an
// error that says how to get one.
func (r defaultResolver) resolveNetwork(name string) (string, error) {
	if name != "" {
		return name, nil
	}
	if name = r.network(); name != "" {
		return name, nil
	}

	networks, err := r.definitions.ListNetworks()
	if err != nil {
		return "", toStatus(err)
	}
	names := make([]string, 0, len(networks))
	for _, n := range networks {
		names = append(names, n.Name)
	}

	return "", noDefaultError("network", names)
}

// noDefaultError explains why an instance that named no kernel or network
// cannot be given one, and what would fix it.
func noDefaultError(kind string, names []string) error {
	if len(names) == 0 {
		made := "created"
		if kind == "kernel" {
			made = "imported"
		}
		return status.Errorf(codes.FailedPrecondition, "no %s given, and none has been %s yet", kind, made)
	}

	return status.Error(codes.InvalidArgument, fmt.Sprintf(
		"no %s given, and no default among the %d %ss (%s): name one, or set defaults.%s in the daemon's configuration",
		kind, len(names), kind, strings.Join(names, ", "), kind))
}
