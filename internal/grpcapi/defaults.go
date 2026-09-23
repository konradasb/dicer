// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"strings"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
)

// Defaults are the configured default kernel and network. Either may be
// empty.
type Defaults struct {
	Kernel  string
	Network string
}

// defaultResolver picks the kernel and network an instance gets when it
// names none: the configured default, or else the only one there is.
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
		return "", err
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
		return "", err
	}
	names := make([]string, 0, len(networks))
	for _, n := range networks {
		names = append(names, n.Name)
	}

	return "", noDefaultError("network", names)
}

// noDefaultError explains why no default kernel or network applies.
func noDefaultError(kind string, names []string) error {
	if len(names) == 0 {
		made := "created"
		if kind == "kernel" {
			made = "imported"
		}
		return errdefs.InvalidState("no %s given, and none has been %s yet", kind, made)
	}

	return errdefs.InvalidArgument(
		"no %s given, and no default among the %d %ss (%s): name one, or set defaults.%s in the daemon's configuration",
		kind, len(names), kind, strings.Join(names, ", "), kind)
}
