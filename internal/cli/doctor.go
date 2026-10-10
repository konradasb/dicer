// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
	"github.com/konradasb/dicer/internal/humanize"
)

// defaultDoctorImage is the test instance's image: small, and quick to pull
// and boot. It is pulled before the checks, so that its progress shows.
const defaultDoctorImage = "docker.io/library/busybox:1.37"

// doctorCheckNames are how results are shown, by name. The test instance's
// boot is shown by its hypervisor instead.
var doctorCheckNames = map[string]string{
	"kvm":           "KVM",
	"ip_forwarding": "IP forwarding",
	"firewall":      "Firewall",
	"tools":         "Tools",
	"uplink":        "Uplink",
	"disk":          "Disk",
	"internet":      "Internet access",
}

// doctorGroupHeadings are the headings results are shown under, by group.
var doctorGroupHeadings = map[dicer.HostCheckGroup]string{
	dicer.HostCheckGroupHost:      "Host",
	dicer.HostCheckGroupInstances: "Instances",
}

func newDoctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that the host can run instances, and boot a test instance",
		Long: "Checks that the daemon's host can run instances and reach them: KVM,\n" +
			"IPv4 forwarding, the firewall, the tools the daemon runs, its uplink and\n" +
			"its free disk. It then boots a small test instance, on cloud-hypervisor\n" +
			"unless --hypervisor-type names another, has it run a command and reach\n" +
			"the internet, and deletes it. Each problem comes with what to do about it.\n\n" +
			"It exits with an error if a check failed. A warning is something that may\n" +
			"go wrong, such as little free disk, and does not.\n\n" +
			"The test instance's image is pulled if the host does not have it: on a\n" +
			"host that cannot reach Docker Hub, name another with --image. Booting a\n" +
			"test instance needs a token with instances:write: with any other, use\n" +
			"--host-only.",
		Example: "  dicer doctor\n" +
			"  dicer doctor --host-only\n" +
			"  dicer doctor --hypervisor-type firecracker\n" +
			"  dicer doctor --image registry.example.com/busybox:1.37",
		Args: noArgs,
		RunE: runDoctor,
	}

	cmd.Flags().String("image", defaultDoctorImage, "Image of the test instance: one with sh and wget, such as busybox")
	cmd.Flags().String("hypervisor-type", "", "Hypervisor of the test instance: cloud-hypervisor or firecracker (default: cloud-hypervisor)")
	cmd.Flags().String("hypervisor-version", "", "Hypervisor version (default: the hypervisor's default version, which 'dicer info' shows)")
	cmd.Flags().Bool("host-only", false, "Check the host only, without booting a test instance")
	cmd.Flags().Duration("timeout", 0, "Give up on a test instance that has not run its command after this long (default: 2m)")
	cmd.Flags().Bool("keep", false, "Keep a test instance that failed, to look into, rather than delete it")
	cmd.Flags().String("format", "table", "Output format: table, json or yaml")
	_ = cmd.RegisterFlagCompletionFunc("format", completeObjectFormats)
	_ = cmd.RegisterFlagCompletionFunc("hypervisor-type", fixedCompletions("cloud-hypervisor", "firecracker"))

	return cmd
}

// doctorReport is what 'dicer doctor' found.
type doctorReport struct {
	// Host is what the daemon is.
	Host dicer.HostInfo `json:"host"`

	// Results is what each check found, the host's first.
	Results []dicer.HostCheckResult `json:"results"`
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	image, _ := cmd.Flags().GetString("image")
	hypervisorVersion, _ := cmd.Flags().GetString("hypervisor-version")
	hostOnly, _ := cmd.Flags().GetBool("host-only")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	keep, _ := cmd.Flags().GetBool("keep")
	format, _ := cmd.Flags().GetString("format")

	var hypervisorType dicer.HypervisorType
	if v, _ := cmd.Flags().GetString("hypervisor-type"); v != "" {
		var err error
		if hypervisorType, err = parseChoice("hypervisor", v, hypervisorTypes); err != nil {
			return err
		}
	}

	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	host, err := client.HostInfo(ctx)
	if err != nil {
		return err
	}
	report := doctorReport{Host: host}
	testHypervisor := doctorHypervisor(host, hypervisorType, hypervisorVersion)
	opts := dicer.CheckHostOptions{
		TestInstance:                  !hostOnly,
		TestInstanceImage:             image,
		TestInstanceHypervisor:        hypervisorType,
		TestInstanceHypervisorVersion: hypervisorVersion,
		TestInstanceTimeout:           timeout,
		KeepFailedTestInstance:        keep,
	}

	// For a person, the test instance's image is pulled first, as dicer run
	// pulls one, so that its progress shows. A spinner then says which check
	// runs. A pull that fails here is left to the daemon, which says what to
	// do about it.
	var progress *doctorProgress
	var onResult func(dicer.HostCheckResult)
	if printer.IsTable(format) {
		progress = &doctorProgress{cmd: cmd}
		if opts.TestInstance {
			_ = ensureImage(cmd, client, image)
			progress.testHypervisor = testHypervisor
		}
		progress.start()
		defer progress.end()
		onResult = progress.advance
	}

	report.Results, err = client.CheckHost(ctx, opts, onResult)
	switch {
	case errors.Is(err, dicer.ErrUnimplemented):
		return errors.New("the daemon is too old to check its host: upgrade it")
	case err != nil:
		return err
	}

	if progress != nil {
		progress.end()
		err = writeDoctor(cmd.OutOrStdout(), report, testHypervisor)
	} else {
		err = writeStructured(cmd.OutOrStdout(), format, report)
	}
	if err != nil {
		return err
	}

	if failed := report.count(dicer.HostCheckStatusFailed); failed > 0 {
		return fmt.Errorf("%s found", humanize.Count(failed, "problem"))
	}
	return nil
}

// count returns how many results have status.
func (r doctorReport) count(status dicer.HostCheckStatus) int {
	n := 0
	for _, result := range r.Results {
		if result.Status == status {
			n++
		}
	}
	return n
}

// doctorProgress shows, on a terminal, which check 'dicer doctor' waits on
// while its results come: a spinner saying what the check does.
type doctorProgress struct {
	cmd *cobra.Command

	// testHypervisor is the hypervisor the test instance boots on, as it is
	// shown, or empty if none is booted.
	testHypervisor string

	// task is the spinner of the check under way.
	task *task
}

// start shows that the host is being checked.
func (d *doctorProgress) start() {
	d.task = startTask(d.cmd, "Checking the host")
}

// advance moves the spinner past a result, on to the check the next one
// waits on. The host's results come together, then the test instance's,
// then at once whether it reached the internet.
func (d *doctorProgress) advance(r dicer.HostCheckResult) {
	d.end()
	if r.Group == dicer.HostCheckGroupHost && d.testHypervisor != "" {
		d.task = startTask(d.cmd, "Booting a test instance on "+d.testHypervisor)
	}
}

// end stops the spinner, if one is drawn.
func (d *doctorProgress) end() {
	if d.task != nil {
		d.task.end()
		d.task = nil
	}
}

// writeDoctor writes what 'dicer doctor' found: a line for each check under
// its group's heading, with what to do about each that did not pass, then a
// summary. testHypervisor is the hypervisor the test instance booted on, as
// it is shown.
func writeDoctor(w io.Writer, r doctorReport, testHypervisor string) error {
	p := paletteFor(w)
	nameWidth := 0
	for _, result := range r.Results {
		nameWidth = max(nameWidth, width(doctorResultName(result, testHypervisor)))
	}
	// Built whole, then written once, so that only the one write can fail.
	var b strings.Builder
	more := func(text string) {
		fmt.Fprintf(&b, "  %s  %s\n", pad("", 2+nameWidth), text)
	}

	fmt.Fprintf(&b, "%s (dicer %s)\n", p.bold(r.Host.Hostname), r.Host.Version)
	var group dicer.HostCheckGroup
	for _, result := range r.Results {
		if result.Group != group {
			group = result.Group
			fmt.Fprintf(&b, "\n%s\n", p.bold(cmp.Or(doctorGroupHeadings[group], string(group))))
		}
		fmt.Fprintf(&b, "  %s %s  %s\n", doctorMark(p, result.Status), pad(doctorResultName(result, testHypervisor), nameWidth), result.Detail)
		for _, line := range result.Console {
			more(line)
		}
		if result.Hint != "" {
			more(result.Hint)
		}
	}

	failed, warned := r.count(dicer.HostCheckStatusFailed), r.count(dicer.HostCheckStatusWarning)
	fmt.Fprintln(&b)
	switch {
	case failed > 0:
		fmt.Fprintln(&b, p.paint(ansiRed, humanize.Count(failed, "problem")+" found."))
	case warned > 0:
		fmt.Fprintln(&b, p.paint(ansiYellow, "No problems, "+humanize.Count(warned, "warning")+"."))
	default:
		fmt.Fprintln(&b, p.paint(ansiGreen, "No problems found."))
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// doctorResultName returns how a result is shown: "KVM", or, for the test
// instance on testHypervisor, "Boot (firecracker v1.17.0)".
func doctorResultName(r dicer.HostCheckResult, testHypervisor string) string {
	if name, ok := doctorCheckNames[r.Name]; ok {
		return name
	}
	if r.Group == dicer.HostCheckGroupInstances {
		return "Boot (" + testHypervisor + ")"
	}
	return r.Name
}

// doctorHypervisor returns a hypervisor's version as it is shown:
// "firecracker v1.17.0". An empty type is the host's default hypervisor, and
// an empty version that hypervisor's default version.
func doctorHypervisor(host dicer.HostInfo, hypervisorType dicer.HypervisorType, version string) string {
	for _, hv := range host.Hypervisors {
		if hv.Type == hypervisorType || hypervisorType == "" && hv.IsDefault {
			hypervisorType = hv.Type
			if version == "" && len(hv.Versions) > 0 {
				version = hv.Versions[0]
			}
			break
		}
	}
	return strings.TrimSpace(string(hypervisorType) + " " + version)
}

// doctorMark returns the mark a result's line starts with.
func doctorMark(p palette, status dicer.HostCheckStatus) string {
	switch status {
	case dicer.HostCheckStatusOK:
		return p.paint(ansiGreen, "✓")
	case dicer.HostCheckStatusWarning:
		return p.paint(ansiYellow, "!")
	default:
		return p.paint(ansiRed, "✗")
	}
}
