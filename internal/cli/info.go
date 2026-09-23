// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"cmp"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
)

// stateOrder is the order instance states are summarised in: what is using
// the host first, what is not last.
var stateOrder = []dicer.InstanceState{
	dicer.StateRunning, dicer.StatePaused, dicer.StateStarting,
	dicer.StateStopping, dicer.StateRestarting, dicer.StateFailed, dicer.StateStopped,
}

func newInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show the daemon, and how much of its host is in use",
		Long: "Shows the daemon, how it is reached, and how much of its host's CPU,\n" +
			"memory and disk is in use.\n\n" +
			"An instance holds its vCPUs and memory while it is starting, running or\n" +
			"paused. A start that would take more than the host allows is refused; what\n" +
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

			host, err := client.GetHostInfo(cmd.Context())
			if err != nil {
				return err
			}
			resources, err := client.GetResources(cmd.Context())
			if err != nil {
				return err
			}
			if format, _ := cmd.Flags().GetString("format"); format != "table" {
				return writeStructured(cmd.OutOrStdout(), format,
					map[string]any{"host": host, "resources": resources})
			}
			instances, err := client.ListInstances(cmd.Context())
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
	w io.Writer, t target, host dicer.HostInfo, resources dicer.HostResources, instances []dicer.Instance,
) error {
	p := paletteFor(w)

	v := &statusView{headline: infoHeadline(host, instances, p)}
	v.block(
		field{"Remote", oneLine(t.String())},
		field{"Network API", networkAPILines(host)},
		field{"Fingerprint", oneLine(host.APIFingerprint)},
	)
	v.block(
		field{"Hypervisors", hypervisorLines(host.Hypervisors)},
		field{"Defaults", defaultsLines(host)},
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
// The fingerprint a client pins is shown beside it: comparing a remote's
// against it is how to check a client is talking to this daemon.
func networkAPILines(host dicer.HostInfo) []string {
	if host.APIFingerprint == "" {
		return []string{"off (set api.tcp.listen to serve it over mutual TLS)"}
	}

	return []string{strings.Join(host.APIAddresses, ", ")}
}

// defaultsLines describe what an instance that names no kernel or network
// gets.
func defaultsLines(host dicer.HostInfo) []string {
	return []string{
		"kernel " + cmp.Or(host.DefaultKernel, "none (name one)"),
		"network " + cmp.Or(host.DefaultNetwork, "none (name one)"),
	}
}

// hypervisorLines describe what an instance may be started with, one
// hypervisor to a line: "cloud-hypervisor v49.0.0 (default), v48.0.0".
func hypervisorLines(hypervisors []dicer.HypervisorInfo) []string {
	if len(hypervisors) == 0 {
		return []string{"none"}
	}

	lines := make([]string, 0, len(hypervisors))
	for _, hv := range hypervisors {
		versions := hv.Versions
		if len(versions) > 0 && hv.IsDefault {
			// The first version is the one an instance gets by default, and
			// the default hypervisor is listed first.
			versions = append([]string{versions[0] + " (default)"}, versions[1:]...)
		}
		lines = append(lines, string(hv.Type)+" "+strings.Join(versions, ", "))
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
	for _, inst := range instances {
		counts[inst.Status.State]++
	}

	parts := make([]string, 0, len(stateOrder))
	for _, state := range stateOrder {
		if n := counts[state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, p.status(state.Lower())))
		}
	}

	return fmt.Sprintf("%s (%d defined)", strings.Join(parts, ", "), len(instances))
}

// healthSummary counts the instances whose health is checked, by verdict:
// " · 2 healthy, 1 unhealthy". Nothing if none is checked.
func healthSummary(instances []dicer.Instance, p palette) string {
	counts := make(map[dicer.HealthStatus]int)
	for _, inst := range instances {
		if h := inst.Status.Health; h != nil && h.Status != "" {
			counts[h.Status]++
		}
	}

	var parts []string
	for _, status := range []dicer.HealthStatus{dicer.HealthUnhealthy, dicer.HealthStarting, dicer.HealthHealthy} {
		if n := counts[status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, p.status(string(status))))
		}
	}
	if len(parts) == 0 {
		return ""
	}

	return " · " + strings.Join(parts, ", ")
}
