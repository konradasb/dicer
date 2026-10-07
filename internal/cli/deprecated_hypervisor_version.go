// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"slices"

	"github.com/konradasb/dicer"
)

// deprecatedHypervisorVersionWarning returns a warning if an instance names a
// hypervisor version the daemon has deprecated, saying what to move to, or
// "" if it does not. It asks the daemon only when a version is named, and
// returns "" if it cannot ask: the warning is advice, and the instance is
// created or changed either way.
func deprecatedHypervisorVersionWarning(
	ctx context.Context, client *dicer.Client, hypervisorType dicer.HypervisorType, version string,
) string {
	if version == "" {
		return ""
	}
	if hypervisorType == "" {
		hypervisorType = dicer.HypervisorTypeCloudHypervisor
	}

	host, err := client.HostInfo(ctx)
	if err != nil {
		return ""
	}
	for _, hypervisor := range host.Hypervisors {
		if hypervisor.Type != hypervisorType || !slices.Contains(hypervisor.DeprecatedVersions, version) {
			continue
		}
		return fmt.Sprintf("Warning: %s %s is deprecated, and a later release of Dicer will remove it. "+
			"Name another version, or none for the default, %s.", hypervisorType, version, hypervisor.Versions[0])
	}
	return ""
}
