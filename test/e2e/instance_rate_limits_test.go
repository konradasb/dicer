// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestInstanceDiskRateLimits checks that each hypervisor holds a guest's
// disk to each of its limits: writes that take well under a second
// unlimited take seconds.
//
// The unit tests check the token buckets each hypervisor is given; only a
// real guest shows the hypervisor enforces them on the disks the guest
// writes.
func TestInstanceDiskRateLimits(t *testing.T) {
	limits := []struct {
		name    string
		flags   []string
		command []string
	}{
		// 16MiB at 4MiB a second, the first second's worth from a full
		// bucket: at least 3 seconds.
		{"bytes", []string{"--disk-rate", "4MiB"}, []string{"dd", "if=/dev/zero", "of=/rate", "bs=1M", "count=16", "conv=fsync"}},
		// 100 direct writes at 25 a second, likewise: at least 3 seconds.
		{"operations", []string{"--disk-iops", "25"}, []string{"dd", "if=/dev/zero", "of=/rate", "bs=4k", "count=100", "oflag=direct"}},
	}
	for _, hypervisor := range []string{"cloud-hypervisor", "firecracker"} {
		for _, limit := range limits {
			t.Run(hypervisor+"/"+limit.name, func(t *testing.T) {
				name := instanceName(t)

				env.createInstance(t, name, append([]string{"--hypervisor-type", hypervisor}, limit.flags...)...)
				env.startInstance(t, name)
				env.waitForAgent(t, name)

				started := time.Now()
				env.exec(t, name, limit.command...)
				if took := time.Since(started); took < 2500*time.Millisecond {
					t.Errorf("%s with %s took %v, want at least 3s", strings.Join(limit.command, " "),
						strings.Join(limit.flags, " "), took)
				}
			})
		}
	}
}

// TestInstanceNetworkRateLimits checks that an instance's bandwidth limits
// are put on its TAP device: its download on the device itself and its
// upload on the IFB device the device's ingress is redirected to.
//
// internal/hostnet's tests check the shaping holds traffic to the rate;
// only a real start shows the instance's limits reach it.
func TestInstanceNetworkRateLimits(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name, "--upload-rate", "1MiB", "--download-rate", "2MiB")
	running := env.startInstance(t, name)
	tap := env.tapDeviceAt(t, running.IP)

	if qdisc := env.onHost(t, "tc", "qdisc", "show", "dev", tap, "root"); !strings.Contains(qdisc, "tbf") ||
		!strings.Contains(qdisc, "rate 16777Kbit") {
		t.Errorf("TAP device %s has root qdisc %q, want a 2MiB/s TBF", tap, qdisc)
	}

	filter := env.onHost(t, "tc", "filter", "show", "dev", tap, "ingress")
	_, ifb, ok := strings.Cut(filter, "to device ")
	if !ok {
		t.Fatalf("TAP device %s redirects nothing from its ingress:\n%s", tap, filter)
	}
	ifb, _, _ = strings.Cut(ifb, ")")
	if qdisc := env.onHost(t, "tc", "qdisc", "show", "dev", ifb, "root"); !strings.Contains(qdisc, "tbf") ||
		!strings.Contains(qdisc, "rate 8388Kbit") {
		t.Errorf("IFB device %s has root qdisc %q, want a 1MiB/s TBF", ifb, qdisc)
	}
}

// onHost runs a command on the host and returns its output, failing the
// test if it does not succeed.
func (e *environment) onHost(t *testing.T, command ...string) string {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	out, err := e.host.run(ctx, command...)
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(command, " "), err)
	}
	return out
}
