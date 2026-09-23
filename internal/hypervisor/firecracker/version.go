// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// Version identifies a supported Firecracker release.
type Version string

// DefaultVersion is the version used when no explicit version is requested.
const DefaultVersion Version = "v1.17.0"

// supportedVersions are the releases Dicer ships binaries for, the default
// first.
var supportedVersions = []Version{DefaultVersion}

// SupportedVersions returns the versions whose binaries are embedded.
func SupportedVersions() []Version {
	return slices.Clone(supportedVersions)
}

// parseVersion runs a firecracker binary to ask its version.
func parseVersion(binaryPath string) (Version, error) {
	// A one-shot local exec of a binary we ship; there is no caller context
	// to honour.
	output, err := exec.Command(binaryPath, "--version").Output() //nolint:noctx // see above
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

// versionFromOutput extracts the version from `firecracker --version`
// output, whose first line reads "Firecracker v1.17.0".
func versionFromOutput(output string) (Version, error) {
	firstLine, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	name, version, ok := strings.Cut(strings.TrimSpace(firstLine), " ")
	if !ok || name != "Firecracker" || !strings.HasPrefix(version, "v") {
		return "", fmt.Errorf("unexpected --version output: %q", firstLine)
	}
	return Version(version), nil
}
