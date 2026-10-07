// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/remote"
)

// testResources is a 4-CPU, 32GiB host with a 1GiB reserve, a quarter of
// its vCPUs and half its memory given out, and a 250GiB disk.
func testResources() dicer.Resources {
	return dicer.Resources{
		CPU: dicer.ResourceCapacity{
			Host: 4, Overcommit: 4, Allocatable: 16, Allocated: 4, Available: 12,
		},
		Memory: dicer.ResourceCapacity{
			Host: 32 << 30, Reserved: 1 << 30, Overcommit: 1,
			Allocatable: 31 << 30, Allocated: 15872 << 20, Available: 15872 << 20,
		},
		Disk: dicer.DiskUsage{
			Path: "/var/lib/dicer", TotalBytes: 250 << 30, FreeBytes: 225 << 30, ProvisionedBytes: 40 << 30,
		},
	}
}

// instancesInStates is a set of instances, one per state given.
func instancesInStates(states ...dicer.InstanceState) []dicer.Instance {
	out := make([]dicer.Instance, 0, len(states))
	for _, state := range states {
		out = append(out, dicer.Instance{State: state})
	}

	return out
}

func TestWriteInfo(t *testing.T) {
	host := dicer.HostInfo{
		Hostname: "compute-1",
		Version:  "v0.5.0",
		Hypervisors: []dicer.HypervisorInfo{
			{
				Type: dicer.HypervisorTypeCloudHypervisor, Versions: []string{"v49.0.0", "v48.0.0"},
				IsDefault: true, DeprecatedVersions: []string{"v48.0.0"},
			},
			{Type: dicer.HypervisorTypeFirecracker, Versions: []string{"v1.17.0"}},
		},
	}
	instances := instancesInStates(dicer.InstanceStateRunning, dicer.InstanceStateRunning, dicer.InstanceStateStopped)
	local := target{name: remote.Local, remote: remote.Remote{Address: dicer.DefaultAddress}}

	var out bytes.Buffer
	if err := writeInfo(&out, local, host, testResources(), instances); err != nil {
		t.Fatalf("writeInfo: %v", err)
	}

	want := `  ┌───────┐
 ╱ ●   ● ╱│
┌───────┐ │
│ ●   ● │●│  dicer v0.5.0
│   ●   │ ┘  compute-1
│ ●   ● │╱   2 running, 1 stopped (3 defined)
└───────┘

         Remote: local (unix:///run/dicer/dicer.sock)
    Network API: off (set api.tcp.listen to serve it over the network)

    Hypervisors: cloud-hypervisor v49.0.0 (default), v48.0.0 (deprecated)
                 firecracker v1.17.0

           vCPU: █████░░░░░░░░░░░░░░░  4 of 16         25%
                 4 CPUs, 4× overcommit
         Memory: ██████████░░░░░░░░░░  15.5 of 31 GiB  50%
                 32 GiB, 1 GiB reserved
           Disk: ██░░░░░░░░░░░░░░░░░░  25 of 250 GiB   10%
                 40 GiB provisioned
`
	if got := out.String(); got != want {
		t.Errorf("writeInfo wrote:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteInfoWithNetworkAPI(t *testing.T) {
	host := dicer.HostInfo{APIAddresses: []string{"192.0.2.1:7443"}}

	var out bytes.Buffer
	if err := writeInfo(&out, target{name: remote.Local, remote: remote.Remote{Address: dicer.DefaultAddress}},
		host, testResources(), nil); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"    Network API: 192.0.2.1:7443\n",
		"no instances defined\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("writeInfo output is missing %q:\n%s", want, out.String())
		}
	}
}

func TestUsageBar(t *testing.T) {
	empty := strings.Repeat("░", barWidth)
	full := strings.Repeat("█", barWidth)

	for _, tc := range []struct {
		used, limit int64
		want        string
	}{
		{0, 16, empty},
		{16, 16, full},
		// Overcommitted past the limit is still only full.
		{20, 16, full},
		// A little in use is drawn, not rounded away.
		{1, 1000, "█" + strings.Repeat("░", barWidth-1)},
		{0, 0, empty},
	} {
		if got := usageBar(tc.used, tc.limit); got != tc.want {
			t.Errorf("usageBar(%d, %d) = %q, want %q", tc.used, tc.limit, got, tc.want)
		}
	}
}

func TestInstanceSummaryOrdersBusiestFirst(t *testing.T) {
	got := instanceSummary(instancesInStates(
		dicer.InstanceStateStopped, dicer.InstanceStateFailed, dicer.InstanceStatePaused,
		dicer.InstanceStateRunning, dicer.InstanceStateRunning, dicer.InstanceStateRestarting,
	), palette{})

	if want := "2 running, 1 paused, 1 restarting, 1 failed, 1 stopped (6 defined)"; got != want {
		t.Errorf("instanceSummary = %q, want %q", got, want)
	}
}

func TestHealthSummary(t *testing.T) {
	health := func(status dicer.HealthStatus) dicer.Instance {
		return dicer.Instance{Health: &dicer.Health{Status: status}}
	}

	got := healthSummary([]dicer.Instance{
		health(dicer.HealthStatusHealthy), health(dicer.HealthStatusUnhealthy), health(dicer.HealthStatusHealthy), {},
	}, palette{})
	if want := " · 1 unhealthy, 2 healthy"; got != want {
		t.Errorf("healthSummary = %q, want %q", got, want)
	}
	if got := healthSummary([]dicer.Instance{{}}, palette{}); got != "" {
		t.Errorf("healthSummary with nothing checked = %q, want nothing", got)
	}
}

// A bar is coloured by how full it is, on a terminal.
func TestResourceBarsAreColouredByLevel(t *testing.T) {
	r := testResources()
	r.CPU.Allocated = 15 // 94%: red
	fields := resourceFields(r, palette{enabled: true})

	if !strings.HasPrefix(fields[0].lines[0], ansiRed) {
		t.Errorf("a nearly full vCPU bar is %q, want it red", fields[0].lines[0])
	}
	if !strings.HasPrefix(fields[2].lines[0], ansiGreen) {
		t.Errorf("a disk with room is %q, want it green", fields[2].lines[0])
	}
}

// On a terminal the die's edges are in the accent colour and its pips bold;
// piped, it is the logo as drawn.
func TestPaintLogoRow(t *testing.T) {
	row := "│ ●   ● │●│"

	if got := paintLogoRow(row, palette{}); got != row {
		t.Errorf("plain = %q, want %q", got, row)
	}

	got := paintLogoRow(row, palette{enabled: true})
	if !strings.HasPrefix(got, ansiCyan+"│ ") || strings.Count(got, ansiBold+logoPip+ansiReset) != 3 {
		t.Errorf("coloured = %q, want cyan edges and three bold pips", got)
	}
}
