// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"fmt"
	"path/filepath"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	"github.com/dicer-sh/dicer/internal/hypervisor/cloudhypervisor"
	"github.com/dicer-sh/dicer/internal/hypervisor/firecracker"
)

// driver is one hypervisor implementation: the versions of it dicerd
// carries, and how to unpack one and drive it.
type driver struct {
	// binary is the name of the VMM executable, and of the directory its
	// versions are extracted into.
	binary string

	// versions are the versions to offer, the default first.
	versions []string

	extract    func(dstPath, version string) (string, error)
	newStarter func(binaryPath string) (hypervisor.Starter, error)
}

// drivers returns the hypervisors dicerd supports.
func drivers() map[dicer.HypervisorType]driver {
	return map[dicer.HypervisorType]driver{
		dicer.HypervisorCloudHypervisor: {
			binary:   "cloud-hypervisor",
			versions: versions(cloudhypervisor.SupportedVersions(), cloudhypervisor.DefaultVersion),
			extract: func(dstPath, version string) (string, error) {
				return cloudhypervisor.Extract(dstPath, cloudhypervisor.Version(version))
			},
			newStarter: func(binaryPath string) (hypervisor.Starter, error) {
				return cloudhypervisor.NewStarter(binaryPath)
			},
		},
		dicer.HypervisorFirecracker: {
			binary:   "firecracker",
			versions: versions(firecracker.SupportedVersions(), firecracker.DefaultVersion),
			extract: func(dstPath, version string) (string, error) {
				return firecracker.Extract(dstPath, firecracker.Version(version))
			},
			newStarter: func(binaryPath string) (hypervisor.Starter, error) {
				return firecracker.NewStarter(binaryPath)
			},
		},
	}
}

// buildStarters extracts every embedded hypervisor binary and returns the
// starters for each type, that type's default version first -- which is the
// one an instance gets when it names no version.
func buildStarters(dataDir string) (map[dicer.HypervisorType][]hypervisor.Starter, error) {
	starters := make(map[dicer.HypervisorType][]hypervisor.Starter)

	for hvType, d := range drivers() {
		for _, version := range d.versions {
			dstPath := filepath.Join(dataDir, "bin", d.binary, version, d.binary)

			binaryPath, err := d.extract(dstPath, version)
			if err != nil {
				return nil, fmt.Errorf("%s %s: extract binary: %w", hvType, version, err)
			}

			starter, err := d.newStarter(binaryPath)
			if err != nil {
				return nil, fmt.Errorf("%s %s: create starter: %w", hvType, version, err)
			}

			starters[hvType] = append(starters[hvType], starter)
		}
	}

	return starters, nil
}

// versions lists a driver's versions as strings with its default first.
func versions[T ~string](all []T, defaultVersion T) []string {
	out := []string{string(defaultVersion)}
	for _, v := range all {
		if v != defaultVersion {
			out = append(out, string(v))
		}
	}
	return out
}
