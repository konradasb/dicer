// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"slices"
	"testing"
)

func TestVersionFromOutput(t *testing.T) {
	// What `firecracker --version` actually prints.
	const output = "Firecracker v1.17.0\n\nSupported snapshot data format versions: 7.0.0\n"

	got, err := versionFromOutput(output)
	if err != nil {
		t.Fatalf("versionFromOutput: %v", err)
	}
	if got != "v1.17.0" {
		t.Errorf("version = %q, want v1.17.0", got)
	}

	for _, bad := range []string{"", "\n", "v1.17.0", "Cloud Hypervisor v48.0.0", "Firecracker"} {
		if _, err := versionFromOutput(bad); err == nil {
			t.Errorf("versionFromOutput(%q) succeeded", bad)
		}
	}
}

func TestSupportedVersionsIncludesDefault(t *testing.T) {
	if !slices.Contains(SupportedVersions(), DefaultVersion) {
		t.Errorf("SupportedVersions() = %v, missing the default %s", SupportedVersions(), DefaultVersion)
	}
}
