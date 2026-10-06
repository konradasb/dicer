// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"slices"
	"testing"
)

// TestVersionFromOutputNamesEveryVersionAlike checks that a version printed
// without its patch number, as v53 prints it, gets one.
func TestVersionFromOutputNamesEveryVersionAlike(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   Version
	}{
		{name: "with patch", output: "cloud-hypervisor v49.0.0\n", want: V49},
		{name: "without patch", output: "cloud-hypervisor v53.0\n", want: V53},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := versionFromOutput(tt.output)
			if err != nil {
				t.Fatalf("versionFromOutput: %v", err)
			}
			if got != tt.want {
				t.Errorf("version = %q, want %q", got, tt.want)
			}
		})
	}

	for _, bad := range []string{"", "\n", "v53.0", "cloud-hypervisor", "Firecracker v1.17.0", "cloud-hypervisor 53.0"} {
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
