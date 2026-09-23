// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// Version identifies a supported Cloud Hypervisor release.
type Version string

// The Cloud Hypervisor releases Dicer ships binaries for.
const (
	V48 Version = "v48.0.0"
	V49 Version = "v49.0.0"
)

// DefaultVersion is the version used when no explicit version is requested.
const DefaultVersion = V49

var supportedVersions = []Version{V48, V49}

// parseVersion runs a cloud-hypervisor binary to ask its version. The binary
// prints a line like "cloud-hypervisor v48.0.0".
func parseVersion(binaryPath string) (Version, error) {
	// A one-shot local exec of a binary we ship; there is no caller context
	// to honour.
	cmd := exec.Command(binaryPath, "--version") //nolint:noctx // see above
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("execute --version: %w", err)
	}

	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return "", fmt.Errorf("unexpected --version output: %q", strings.TrimSpace(string(output)))
	}

	v := Version(fields[1])
	if !slices.Contains(supportedVersions, v) {
		return "", fmt.Errorf("unsupported version %s", v)
	}

	return v, nil
}

// SupportedVersions returns the versions whose binaries are embedded.
func SupportedVersions() []Version {
	return slices.Clone(supportedVersions)
}
