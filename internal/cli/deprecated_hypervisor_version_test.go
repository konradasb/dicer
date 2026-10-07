// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"strings"
	"testing"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// TestDeprecatedHypervisorVersionIsWarnedOf covers the warning the command
// line prints when an instance names a deprecated hypervisor version, and its
// absence when the instance names the default or none.
func TestDeprecatedHypervisorVersionIsWarnedOf(t *testing.T) {
	const warning = "Warning: cloud-hypervisor v49.0.0 is deprecated, and a later release of Dicer will remove it. " +
		"Name another version, or none for the default, v53.0.0."

	tests := []struct {
		name     string
		instance *dicerdv1.Instance
		args     []string
		warned   bool
	}{
		{name: "run with a deprecated version", args: []string{"run", "-d", "--name", "a", "--hypervisor-version", "v49.0.0", "alpine:3.21"}, warned: true},
		{name: "create with a deprecated version", args: []string{"create", "a", "-i", "alpine:3.21", "--hypervisor-version", "v49.0.0"}, warned: true},
		{name: "run with the default version", args: []string{"run", "-d", "--name", "a", "--hypervisor-version", "v53.0.0", "alpine:3.21"}},
		{name: "run with no version", args: []string{"run", "-d", "--name", "a", "alpine:3.21"}},
		{
			name:     "update of an instance on a deprecated version",
			instance: &dicerdv1.Instance{Id: "id-a", Name: "a", State: stateStopped, HypervisorVersion: "v49.0.0"},
			args:     []string{"update", "a", "--memory", "1GiB"},
			warned:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfig(t)
			var d *fakeInstanceDaemon
			if tt.instance != nil {
				d = newFakeInstanceDaemon(tt.instance)
			} else {
				d = newFakeInstanceDaemon()
			}
			d.cached["alpine:3.21"] = true
			d.host.Hypervisors = []*dicerdv1.HypervisorInfo{{
				Type:               dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR,
				Versions:           []string{"v53.0.0", "v49.0.0"},
				IsDefault:          true,
				DeprecatedVersions: []string{"v49.0.0"},
			}}
			serveFakeDaemon(t, d)

			out, err := run(t, tt.args...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", tt.args, err, out)
			}
			if got := strings.Contains(out, warning); got != tt.warned {
				t.Errorf("warned = %t, want %t:\n%s", got, tt.warned, out)
			}
		})
	}
}
