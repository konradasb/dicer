// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
)

// stateOrder is the order instance states are summarised in: what is using
// the host first, what is not last.
var stateOrder = []dicer.InstanceState{
	dicer.InstanceStateRunning, dicer.InstanceStatePaused, dicer.InstanceStateStarting, dicer.InstanceStateStopping,
	dicer.InstanceStateRestarting, dicer.InstanceStateStandby, dicer.InstanceStateFailed, dicer.InstanceStateStopped,
}

func newInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show the daemon, and how much of its host is in use",
		Long: "Shows the daemon, how it is reached, and how much of its host's CPU,\n" +
			"memory and disk is in use.\n\n" +
			"An instance's vCPUs and memory are committed to it while it is starting,\n" +
			"running or paused. A start that would take more than the host allows is refused; what\n" +
			"it allows is its CPUs and memory, less a reserve, stretched by the\n" +
			"overcommit set in the daemon's configuration. Disk is reported, not\n" +
			"enforced: disks are sparse, and what an instance has been given says\n" +
			"little about what it will write.\n\n" +
			"With --format json or yaml, prints the daemon's own records of the host\n" +
			"and its resources, for scripts.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := resolveTarget(cmd)
			if err != nil {
				return err
			}

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			host, err := client.HostInfo(cmd.Context())
			if err != nil {
				return err
			}
			resources, err := client.Resources(cmd.Context())
			if err != nil {
				return err
			}
			if format, _ := cmd.Flags().GetString("format"); !printer.IsTable(format) {
				hostRecord, err := record(host)
				if err != nil {
					return err
				}
				resourcesRecord, err := record(resources)
				if err != nil {
					return err
				}
				return writeStructured(cmd.OutOrStdout(), format,
					map[string]any{"host": hostRecord, "resources": resourcesRecord})
			}
			instances, err := client.Instances.List(cmd.Context())
			if err != nil {
				return err
			}

			return writeInfo(cmd.OutOrStdout(), t, host, resources, instances)
		},
	}

	cmd.Flags().String("format", "table", "Output format: table, json or yaml")
	_ = cmd.RegisterFlagCompletionFunc("format", completeFormats)

	return cmd
}

// writeInfo writes what 'dicer info' shows, laid out as inspect lays out an
// instance: a headline with what the daemon is at a glance, then how it is
// reached, what it starts instances with, and how full its host is.
//
//	  ┌───────┐
//	 ╱ ●   ● ╱│
//	┌───────┐ │
//	│ ●   ● │●│  dicer v0.5.0
//	│   ●   │ ┘  compute-1
//	│ ●   ● │╱   2 running, 1 stopped (3 defined) · 2 healthy
//	└───────┘
//
//	         Remote: local (unix:///run/dicer/dicer.sock)
//	    Network API: 192.0.2.1:7443
//	    ...
//	           vCPU: ████░░░░░░░░░░░░░░░░  4 of 16         25%
//	                 4 CPUs, 4× overcommit
func writeInfo(
	w io.Writer, t target, host dicer.HostInfo, resources dicer.Resources, instances []dicer.Instance,
) error {
	p := paletteFor(w)

	v := &statusView{headline: infoHeadline(host, instances, p)}
	v.block(
		field{"Remote", oneLine(t.String())},
		field{"Network API", networkAPILines(host)},
	)
	v.block(
		field{"Hypervisors", hypervisorLines(host.Hypervisors)},
	)
	v.block(resourceFields(resources, p)...)

	return v.write(w)
}

// logo is Dicer's mark: a die in three dimensions, five on its face, two on
// top and one on its side.
var logo = []string{
	"  ┌───────┐",
	" ╱ ●   ● ╱│",
	"┌───────┐ │",
	"│ ●   ● │●│",
	"│   ●   │ ┘",
	"│ ●   ● │╱",
	"└───────┘",
}

// logoPip is a pip of the die, drawn apart from its edges.
const logoPip = "●"

// infoHeadline is the logo, with what the daemon is beside the die's face:
// its version, its host, and what its instances are doing.
func infoHeadline(host dicer.HostInfo, instances []dicer.Instance, p palette) string {
	beside := map[int]string{
		3: p.bold("dicer") + " " + host.Version,
		4: host.Hostname,
		5: instanceSummary(instances, p) + healthSummary(instances, p),
	}

	width := 0
	for _, row := range logo {
		width = max(width, utf8.RuneCountInString(row))
	}

	lines := make([]string, len(logo))
	for i, row := range logo {
		line := paintLogoRow(row, p)
		if text := beside[i]; text != "" {
			line += strings.Repeat(" ", width-utf8.RuneCountInString(row)) + "  " + text
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// paintLogoRow draws a row of the logo: its edges in the accent colour, its
// pips in bold.
func paintLogoRow(row string, p palette) string {
	parts := strings.Split(row, logoPip)
	for i, part := range parts {
		parts[i] = p.accent(part)
	}
	return strings.Join(parts, p.bold(logoPip))
}

// networkAPILines describe whether the API is served over TCP, and where.
func networkAPILines(host dicer.HostInfo) []string {
	if len(host.APIAddresses) == 0 {
		return []string{"off (set api.tcp.listen to serve it over the network)"}
	}

	return []string{strings.Join(host.APIAddresses, ", ")}
}

// hypervisorLines describe what an instance may be started with, one
// hypervisor to a line: "cloud-hypervisor v53.0.0 (default), v49.0.0
// (deprecated)".
func hypervisorLines(hypervisors []dicer.HypervisorInfo) []string {
	if len(hypervisors) == 0 {
		return []string{"none"}
	}

	lines := make([]string, 0, len(hypervisors))
	for _, hypervisor := range hypervisors {
		versions := make([]string, 0, len(hypervisor.Versions))
		for i, v := range hypervisor.Versions {
			switch {
			case i == 0 && hypervisor.IsDefault:
				// The first version is the one an instance gets by default,
				// and the default hypervisor is listed first.
				v += " (default)"
			case slices.Contains(hypervisor.DeprecatedVersions, v):
				v += " (deprecated)"
			}
			versions = append(versions, v)
		}
		lines = append(lines, string(hypervisor.Type)+" "+strings.Join(versions, ", "))
	}

	return lines
}

// instanceSummary counts instances by state, busiest first: "2 running, 1
// stopped (3 defined)".
func instanceSummary(instances []dicer.Instance, p palette) string {
	if len(instances) == 0 {
		return "no instances defined"
	}

	counts := make(map[dicer.InstanceState]int, len(stateOrder))
	for _, instance := range instances {
		counts[instance.State]++
	}

	parts := make([]string, 0, len(stateOrder))
	for _, state := range stateOrder {
		if n := counts[state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, p.status(string(state))))
		}
	}

	return fmt.Sprintf("%s (%d defined)", strings.Join(parts, ", "), len(instances))
}

// healthSummary counts the instances whose health is checked, by verdict:
// " · 2 healthy, 1 unhealthy". Nothing if none is checked.
func healthSummary(instances []dicer.Instance, p palette) string {
	counts := make(map[dicer.HealthStatus]int)
	for _, instance := range instances {
		if instance.Health != nil && instance.Health.Status != "" {
			counts[instance.Health.Status]++
		}
	}

	var parts []string
	for _, status := range []dicer.HealthStatus{dicer.HealthStatusUnhealthy, dicer.HealthStatusStarting, dicer.HealthStatusHealthy} {
		if n := counts[status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, p.status(string(status))))
		}
	}
	if len(parts) == 0 {
		return ""
	}

	return " · " + strings.Join(parts, ", ")
}
