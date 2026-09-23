// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
)

type printableInstance struct {
	Instances []dicer.Instance
}

func (p *printableInstance) Cols() []string {
	return []string{
		"Name", "Image", "State", "Status", "VCPU", "Memory", "Disk", "Network", "IP", "Ports", "Created",
	}
}

// DefaultCols are what a table shows unless asked for more: enough to
// see what is running and how to reach it, in a terminal's width.
func (p *printableInstance) DefaultCols() []string {
	return []string{"Name", "Image", "Status", "IP", "Ports"}
}

func (p *printableInstance) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Instances))
	for _, inst := range p.Instances {
		state := inst.Status.State.String()
		if e := inst.Status.StateError; e != "" {
			state += " (" + firstLine(e) + ")"
		}

		kv = append(kv, map[string]any{
			"Name":    inst.Spec.Name,
			"Image":   inst.Spec.ImageRef,
			"State":   state,
			"Status":  instanceStatus(inst),
			"VCPU":    inst.Spec.VCPUs,
			"Memory":  size(inst.Spec.MemoryBytes),
			"Disk":    size(inst.Spec.DiskBytes),
			"Network": inst.Spec.NetworkName,
			"IP":      orDash(inst.Status.IP),
			"Ports":   orDash(formatPorts(inst.Spec.Ports)),
			"Created": age(inst.Spec.CreatedAt),
		})
	}
	return kv
}

// instanceStatus describes an instance's state as docker ps does, with how
// long it has been up or since it ended, and its health if it is checked:
// "Up 3 minutes (healthy)", "Exited (1) 2 minutes ago", "Restarting (3) in 8
// seconds", "Failed: no such kernel".
func instanceStatus(inst dicer.Instance) string {
	status := inst.Status
	switch state := status.State; state {
	case dicer.StateRunning:
		if !status.StartedAt.IsZero() {
			return "Up " + units.HumanDuration(time.Since(status.StartedAt)) + healthSuffix(inst)
		}

		return "Up" + healthSuffix(inst)
	case dicer.StateRestarting:
		out := fmt.Sprintf("Restarting (%d)", status.RestartCount)
		if wait := time.Until(status.NextRestartAt); !status.NextRestartAt.IsZero() && wait >= time.Second {
			out += " in " + units.HumanDuration(wait)
		}

		return out
	case dicer.StateStopped, dicer.StateFailed:
		// An instance whose workload exited says so, and when, as a
		// container would.
		if status.ExitCode != nil && !status.FinishedAt.IsZero() {
			return fmt.Sprintf("Exited (%d) %s", *status.ExitCode, age(status.FinishedAt))
		}
		if e := status.StateError; e != "" && state == dicer.StateFailed {
			return "Failed: " + firstLine(e)
		}

		return state.String()
	default:
		return state.String()
	}
}

// formatPorts renders published ports as Docker does, e.g.
// "8080->80/tcp, 10.0.0.1:53->53/udp".
func formatPorts(ports []dicer.PortMapping) string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		s := fmt.Sprintf("%d->%d/%s", p.HostPort, p.GuestPort, p.Proto())
		if p.HostIP != "" {
			s = p.HostIP + ":" + s
		}
		out = append(out, s)
	}

	return strings.Join(out, ", ")
}

// firstLine trims a multi-line error down to something that fits in a table
// cell.
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	const maxLen = 60
	if len(s) > maxLen {
		s = s[:maxLen-1] + "…"
	}
	return s
}

func newInstanceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "instance",
		Short:   "Manage instances",
		Aliases: []string{"instances", "vm"},
	}

	cmd.AddCommand(
		newInstanceCreateCommand(),
		newInstanceRunCommand(),
		newInstanceUpdateCommand(),
		newInstanceRenameCommand(),
		newInstanceStartCommand(),
		newInstanceStopCommand(),
		newInstanceRestartCommand(),
		newInstancePauseCommand(),
		newInstanceResumeCommand(),
		newInstanceDeleteCommand(),
		newInstanceListCommand(),
		newInstanceShowCommand(),
		newInstanceExecCommand(),
		newInstanceCopyCommand(),
		newInstanceLogsCommand(),
		newInstanceWaitCommand(),
		newInstanceSnapshotCommand(),
	)

	return cmd
}

func newInstanceCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [NAME] [-- COMMAND [ARG...]]",
		Short: "Define an instance without starting it",
		Long: "Records an instance definition. Nothing is booted until you run\n" +
			"'dicer start', unless --start is given.\n\n" +
			"A command after -- replaces the image's ENTRYPOINT and CMD.",
		Example: "  dicer instance create web -i nginx:1.27 --network default -p 8080:80\n" +
			"  dicer instance create -f web.yaml --start\n" +
			"  dicer instance create worker -i alpine:3.21 -e QUEUE=jobs -- /bin/worker --verbose",
		Args:    createArgs,
		Aliases: []string{"new", "define"},
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, start, err := buildCreateRequest(cmd, args)
			if err != nil {
				return err
			}

			return createInstance(cmd, spec, start)
		},
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().StringP("file", "f", "", "Read the definition from a YAML file ('-' for stdin)")
	cmd.Flags().StringP("image", "i", "", "Container image reference, e.g. docker.io/library/ubuntu:24.04")
	_ = cmd.RegisterFlagCompletionFunc("image", complete(0, listImages))
	addInstanceSpecFlags(cmd, true)
	cmd.Flags().Bool("start", false, "Start the instance immediately after defining it")

	return cmd
}

func newInstanceRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [flags] IMAGE [COMMAND [ARG...]]",
		Short: "Create an instance from an image and start it",
		Long: "Defines an instance from an image and boots it, pulling the image first if\n" +
			"it is not on the host yet. The instance is named after the image unless\n" +
			"--name is given.\n\n" +
			"A command after the image replaces its ENTRYPOINT and CMD. Flags go before\n" +
			"the image: everything after it is the command's.",
		Example: "  dicer run nginx:1.27\n" +
			"  dicer run --name web -p 8080:80 --memory 1GiB nginx:1.27\n" +
			"  dicer run --vcpus 2 alpine:3.21 sleep infinity",
		Args: oneThenCommand("an image"),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := buildRunRequest(cmd, args)
			if err != nil {
				return err
			}

			return createInstance(cmd, spec, true)
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				// The image's command, which only the guest knows.
				return nil, cobra.ShellCompDirectiveDefault
			}
			return complete(1, listImages)(cmd, args, toComplete)
		},
	}

	// Everything after the image is the guest command's, flags and all, as
	// with docker run.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().SortFlags = false
	cmd.Flags().String("name", "", "Instance name (default: the image's name and a random suffix)")
	addInstanceSpecFlags(cmd, true)
	cmd.Flags().BoolP("detach", "d", true, "Accepted for Docker compatibility; instances always run detached")
	_ = cmd.Flags().MarkHidden("detach")

	return cmd
}

func newInstanceUpdateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update NAME [-- COMMAND [ARG...]]",
		Short: "Change a stopped instance's definition",
		Long: "Changes the parts of a stopped instance's definition that are given, and\n" +
			"leaves the rest as it was. A list or map given -- --env, --label,\n" +
			"--publish, --volume, --host-file -- replaces the old one whole.\n\n" +
			"A command after -- replaces the one the instance runs.\n\n" +
			"The restart policy alone can be changed while the instance runs: it applies\n" +
			"the next time the instance ends.",
		Example: "  dicer update web --memory 2GiB --vcpus 2\n" +
			"  dicer update web --restart unless-stopped\n" +
			"  dicer update web -- /usr/sbin/nginx -g 'daemon off;'",
		Args: func(cmd *cobra.Command, args []string) error {
			return one("an instance name")(cmd, positionalArgs(cmd, args))
		},
		ValidArgsFunction: complete(1, instancesIn(dicer.StateStopped, dicer.StateFailed)),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch, err := buildUpdateRequest(cmd, args)
			if err != nil {
				return err
			}

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			inst, err := client.UpdateInstance(cmd.Context(), patch)
			if err != nil {
				return suggest(cmd.Context(), client, instancesIn(), patch.Name, err)
			}

			succeeded(cmd, "Instance %s updated", inst.Spec.Name)

			return nil
		},
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().StringP("image", "i", "", "Container image reference")
	_ = cmd.RegisterFlagCompletionFunc("image", complete(0, listImages))
	addInstanceSpecFlags(cmd, false)

	return cmd
}

// createInstance sends a create request and says what came of it. An
// instance to be started has its image pulled first, so that the download
// shows as it happens rather than as a start that hangs.
func createInstance(cmd *cobra.Command, spec dicer.InstanceSpec, start bool) error {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	if !start {
		inst, err := client.CreateInstance(cmd.Context(), spec)
		if err != nil {
			return err
		}
		succeeded(cmd, "Instance %s created. Start it with: dicer start %s", inst.Spec.Name, inst.Spec.Name)

		return nil
	}

	if err := ensureImage(cmd, client, spec.ImageRef); err != nil {
		return err
	}

	err = runTask(cmd, "Starting "+spec.Name, func() (dicer.Instance, error) {
		return client.RunInstance(cmd.Context(), spec)
	}, func(inst dicer.Instance, took string) string {
		return fmt.Sprintf("Instance %s started in %s (%s)", inst.Spec.Name, took, orDash(inst.Status.IP))
	})
	if err != nil {
		return err
	}

	if isTerminal(cmd.ErrOrStderr()) {
		cmd.PrintErrf("  Shell: dicer exec %[1]s   Logs: dicer logs -f %[1]s   Stop: dicer stop %[1]s\n",
			spec.Name)
	}

	return nil
}

// ensureImage pulls an image the host does not hold, showing the download
// and saying so once it is done. An image already on the host is used as it
// is, as the daemon's start would: no registry is asked, so a start works
// offline, or while a registry is limiting the host's pulls.
func ensureImage(cmd *cobra.Command, client *dicer.Client, ref string) error {
	_, err := client.GetImage(cmd.Context(), ref)
	if err == nil {
		return nil
	}
	if !errors.Is(err, dicer.ErrNotFound) {
		return err
	}

	// Off a terminal, the stages of a pull that is only a check would be
	// noise in a log: a download is reported once it is done.
	progress := cmd.ErrOrStderr()
	if !isTerminal(progress) {
		progress = io.Discard
	}

	reporter := newPullReporter(progress)
	defer reporter.done()

	start := time.Now()
	img, err := client.PullImage(cmd.Context(), ref, reporter.report)
	if err != nil {
		return err
	}
	reporter.done()

	if reporter.fetched {
		succeeded(cmd, "Image %s pulled in %s (%s)", img.Name, formatDuration(time.Since(start)), size(img.SizeBytes))
	}

	return nil
}

// osCause strips the operation and path from a file error, for a message
// that names the file itself: "cannot read vm.yaml: no such file or
// directory", not "... vm.yaml: open vm.yaml: no such file or directory".
func osCause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
