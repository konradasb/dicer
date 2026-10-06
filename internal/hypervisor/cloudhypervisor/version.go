// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// Version identifies a supported Cloud Hypervisor release.
type Version string

// The Cloud Hypervisor releases Dicer ships binaries for.
const (
	V48 Version = "v48.0.0"
	V49 Version = "v49.0.0"
	V53 Version = "v53.0.0"
)

// DefaultVersion is the version used when no explicit version is requested.
const DefaultVersion = V53

// supportedVersions are the releases Dicer ships binaries for, newest first.
var supportedVersions = []Version{V53, V49, V48}

// restoresMemoryOnDemand reports whether the version can restore a guest's
// memory on demand. v52 restores each page when the guest first uses it, but
// only v53 also restores the rest in the background. Without that, the
// restore may never finish, and Cloud Hypervisor refuses every snapshot of
// the guest until it does. So v53 is the first version used this way.
func (v Version) restoresMemoryOnDemand() bool {
	major, _, _ := strings.Cut(strings.TrimPrefix(string(v), "v"), ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= 53
}

// parseVersion runs a cloud-hypervisor binary to ask its version.
func parseVersion(binaryPath string) (Version, error) {
	// A one-shot local exec of a binary we ship; there is no caller context
	// to honour.
	cmd := exec.Command(binaryPath, "--version") //nolint:noctx // see above
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("execute --version: %w", err)
	}

	v, err := versionFromOutput(string(output))
	if err != nil {
		return "", err
	}
	if !slices.Contains(supportedVersions, v) {
		return "", fmt.Errorf("unsupported version %s", v)
	}

	return v, nil
}

// versionFromOutput extracts the version from `cloud-hypervisor --version`
// output, which reads "cloud-hypervisor v49.0.0" or, from v53 on,
// "cloud-hypervisor v53.0". A version without its patch number is given
// one, so that every version is named alike.
func versionFromOutput(output string) (Version, error) {
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[0] != "cloud-hypervisor" || !strings.HasPrefix(fields[1], "v") {
		return "", fmt.Errorf("unexpected --version output: %q", strings.TrimSpace(output))
	}

	version := fields[1]
	if strings.Count(version, ".") == 1 {
		version += ".0"
	}
	return Version(version), nil
}

// SupportedVersions returns the versions whose binaries are embedded.
func SupportedVersions() []Version {
	return slices.Clone(supportedVersions)
}
