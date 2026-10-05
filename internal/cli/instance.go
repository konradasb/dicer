// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
	"github.com/konradasb/dicer/internal/humanize"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

type printableInstance struct {
	Instances []*dicerdv1.Instance
}

func (p *printableInstance) Columns() []string {
	return []string{
		"Name", "Image", "State", "Status", "VCPU", "Memory", "Disk", "Network", "IP", "Ports", "Created",
	}
}

// DefaultColumns are what a table shows unless asked for more: enough to
// see what is running and how to reach it, in a terminal's width.
func (p *printableInstance) DefaultColumns() []string {
	return []string{"Name", "Image", "Status", "IP", "Ports"}
}

func (p *printableInstance) Rows() []map[string]any {
	rows := make([]map[string]any, 0, len(p.Instances))
	for _, instance := range p.Instances {
		state := stateName(instance.GetState())
		if e := instance.GetStateError(); e != "" {
			state += " (" + firstLine(e) + ")"
		}

		rows = append(rows, map[string]any{
			"Name":    instance.GetName(),
			"Image":   instance.GetImageRef(),
			"State":   state,
			"Status":  instanceStatus(instance),
			"VCPU":    instance.GetVcpus(),
			"Memory":  humanize.Bytes(instance.GetMemoryBytes()),
			"Disk":    humanize.Bytes(instance.GetDiskBytes()),
			"Network": instance.GetNetworkName(),
			"IP":      orDash(instance.GetIp()),
			"Ports":   orDash(formatPorts(instance.GetPorts())),
			"Created": age(timeOf(instance.GetCreateTime())),
		})
	}
	return rows
}

// instanceStatus describes an instance's state as docker ps does, with how
// long it has been up or since it ended, and its health if it is checked:
// "Up 3 minutes (healthy)", "Exited (1) 2 minutes ago", "Restarting (3) in 8
// seconds", "Failed: no such kernel".
func instanceStatus(instance *dicerdv1.Instance) string {
	switch state := instance.GetState(); state {
	case stateRunning:
		if started := timeOf(instance.GetStartTime()); !started.IsZero() {
			return "Up " + units.HumanDuration(time.Since(started)) + healthSuffix(instance)
		}

		return "Up" + healthSuffix(instance)
	case stateRestarting:
		out := fmt.Sprintf("Restarting (%d)", instance.GetRestartCount())
		if next := timeOf(instance.GetNextRestartTime()); !next.IsZero() && time.Until(next) >= time.Second {
			out += " in " + units.HumanDuration(time.Until(next))
		}

		return out
	case stateStopped, stateFailed:
		// An instance whose workload exited says so, and when, as a
		// container would.
		if finished := timeOf(instance.GetFinishTime()); instance.ExitCode != nil && !finished.IsZero() {
			return fmt.Sprintf("Exited (%d) %s", instance.GetExitCode(), age(finished))
		}
		if e := instance.GetStateError(); e != "" && state == stateFailed {
			return "Failed: " + firstLine(e)
		}

		return stateName(state)
	default:
		return stateName(state)
	}
}

// formatPorts renders published ports as Docker does, e.g.
// "8080->80/tcp, 10.0.0.1:53->53/udp".
func formatPorts(ports []*dicerdv1.PortMapping) string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		s := fmt.Sprintf("%d->%d/%s", p.GetHostPort(), p.GetGuestPort(), protocolName(p.GetProtocol()))
		if p.GetHostIp() != "" {
			s = p.GetHostIp() + ":" + s
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
		newInstanceResizeCommand(),
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
		newInstanceStatsCommand(),
		newInstanceTopCommand(),
		newInstanceWaitCommand(),
	)

	return cmd
}

func newInstanceCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [NAME] [-- COMMAND [ARG...]]",
		Short: "Define an instance without starting it",
		Long: "Records an instance definition, pulling the image first as --pull says:\n" +
			"by default, only if the host does not hold it. Nothing is booted until\n" +
			"you run 'dicer start', unless --start is given.\n\n" +
			"A command after -- replaces the image's ENTRYPOINT and CMD.",
		Example: "  dicer instance create web -i nginx:1.27 --network default -p 8080:80\n" +
			"  dicer instance create web -i nginx:1.27 --start\n" +
			"  dicer instance create worker -i alpine:3.21 -e QUEUE=jobs -- /bin/worker --verbose",
		Args:    createArgs,
		Aliases: []string{"new", "define"},
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := buildCreateRequest(cmd, args)
			if err != nil {
				return err
			}

			return createInstance(cmd, req)
		},
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().StringP("image", "i", "", "Container image reference, e.g. docker.io/library/ubuntu:24.04")
	_ = cmd.RegisterFlagCompletionFunc("image", complete(0, listImages))
	addInstanceSpecFlags(cmd, true)
	addPullFlag(cmd, "When to pull the image")
	cmd.Flags().Bool("start", false, "Start the instance immediately after defining it")

	return cmd
}

func newInstanceRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [flags] IMAGE [COMMAND [ARG...]]",
		Short: "Create an instance from an image and start it",
		Long: "Defines an instance from an image and boots it, pulling the image first as\n" +
			"--pull says: by default, only if the host does not hold it. The instance is\n" +
			"named after the image unless --name is given.\n\n" +
			"A command after the image replaces its ENTRYPOINT and CMD. Flags go before\n" +
			"the image: everything after it is the command's.\n\n" +
			"As with docker run, the guest's console is written out until the instance\n" +
			"stops, and the command exits with the status it ended with. Ctrl+C stops\n" +
			"the instance; a second Ctrl+C stops waiting for it. With -d the instance\n" +
			"runs in the background instead.",
		Example: "  dicer run --rm alpine:3.21 echo hello\n" +
			"  dicer run -d --name web -p 8080:80 --memory 1GiB nginx:1.27\n" +
			"  dicer run -d --vcpus 2 alpine:3.21 sleep infinity",
		Args: oneThenCommand("an image"),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := buildRunRequest(cmd, args)
			if err != nil {
				return err
			}

			if detach, _ := cmd.Flags().GetBool("detach"); detach {
				return createInstance(cmd, req)
			}
			return runAttached(cmd, req)
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
	cmd.Flags().BoolP("detach", "d", false, "Run in the background: print nothing of the console and return once started")
	cmd.Flags().String("name", "", "Instance name (default: the image's name and a random suffix)")
	addInstanceSpecFlags(cmd, true)
	addPullFlag(cmd, "When to pull the image")

	return cmd
}

func newInstanceUpdateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update NAME [-- COMMAND [ARG...]]",
		Short: "Change a stopped instance's definition",
		Long: "Changes the parts of a stopped instance's definition that are given, and\n" +
			"leaves the rest as it was. A list or map given -- --env, --label,\n" +
			"--publish, --mount -- replaces the old one whole.\n\n" +
			"A command after -- replaces the one the instance runs.\n\n" +
			"The restart policy alone can be changed while the instance runs: it applies\n" +
			"the next time the instance ends.",
		Example: "  dicer update web --memory 2GiB --vcpus 2\n" +
			"  dicer update web --restart unless-stopped\n" +
			"  dicer update web -- /usr/sbin/nginx -g 'daemon off;'",
		Args: func(cmd *cobra.Command, args []string) error {
			return one("an instance name")(cmd, positionalArgs(cmd, args))
		},
		ValidArgsFunction: complete(1, instancesIn(stateStopped, stateFailed)),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := buildUpdateRequest(cmd, args)
			if err != nil {
				return err
			}

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			instance, err := client.UpdateInstance(cmd.Context(), req)
			if err != nil {
				return suggest(cmd.Context(), client, instancesIn(), req.GetName(), err)
			}

			succeeded(cmd, "Instance %s updated", instance.GetName())

			return nil
		},
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().StringP("image", "i", "", "Container image reference")
	_ = cmd.RegisterFlagCompletionFunc("image", complete(0, listImages))
	addInstanceSpecFlags(cmd, false)

	return cmd
}

// createInstance sends a create request and reports the result. The image
// is pulled first, as the request's pull policy says, so that its progress
// shows.
func createInstance(cmd *cobra.Command, req *dicerdv1.CreateInstanceRequest) error {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	if req.PullPolicy, err = pullAsPolicy(cmd, client, req.GetImageRef(), req.GetPullPolicy()); err != nil {
		return err
	}

	if !req.GetStart() {
		instance, err := client.CreateInstance(cmd.Context(), req)
		if err != nil {
			return err
		}
		succeeded(cmd, "Instance %s created. Start it with: dicer start %s", instance.GetName(), instance.GetName())

		return nil
	}

	err = runTask(cmd, "Starting "+req.GetName(), func() (*dicerdv1.Instance, error) {
		return client.CreateInstance(cmd.Context(), req)
	}, func(instance *dicerdv1.Instance, took string) string {
		return fmt.Sprintf("Instance %s started in %s (%s)", instance.GetName(), took, orDash(instance.GetIp()))
	})
	if err != nil {
		return err
	}

	if isTerminal(cmd.ErrOrStderr()) {
		cmd.PrintErrf("  Shell: dicer exec %[1]s   Logs: dicer logs -f %[1]s   Stop: dicer stop %[1]s\n",
			req.GetName())
	}

	return nil
}

// pullAsPolicy pulls an image as policy says, showing its progress, since
// the daemon's own pull shows none. It returns the policy to create the
// instance with: once pulled here, the daemon need only find the image.
func pullAsPolicy(
	cmd *cobra.Command, client *dicer.Client, ref string, policy dicerdv1.PullPolicy,
) (dicerdv1.PullPolicy, error) {
	var err error
	switch policy {
	case dicerdv1.PullPolicy_PULL_POLICY_NEVER:
		return policy, nil
	case dicerdv1.PullPolicy_PULL_POLICY_ALWAYS:
		err = pullShowingProgress(cmd, client, ref)
	default:
		err = ensureImage(cmd, client, ref)
	}
	if err != nil {
		return policy, err
	}

	return dicerdv1.PullPolicy_PULL_POLICY_MISSING, nil
}

// ensureImage pulls an image the host does not hold, showing progress.
func ensureImage(cmd *cobra.Command, client *dicer.Client, ref string) error {
	_, err := client.GetImage(cmd.Context(), &dicerdv1.GetImageRequest{Ref: ref})
	if err == nil {
		return nil
	}
	if status.Code(err) != codes.NotFound {
		return err
	}

	return pullShowingProgress(cmd, client, ref)
}

// pullShowingProgress pulls an image, showing its progress on a terminal,
// and says so once it is done if anything was downloaded.
func pullShowingProgress(cmd *cobra.Command, client *dicer.Client, ref string) error {
	// Off a terminal, the stages of a pull that is only a check would be
	// noise in a log: a download is reported once it is done.
	progress := cmd.ErrOrStderr()
	if !isTerminal(progress) {
		progress = io.Discard
	}

	reporter := newPullReporter(progress)
	defer reporter.done()

	start := time.Now()
	image, err := pullImage(cmd.Context(), client, ref, reporter.report)
	if err != nil {
		return err
	}
	reporter.done()

	if reporter.fetched {
		succeeded(cmd, "Image %s pulled in %s (%s)",
			image.GetName(), humanize.Duration(time.Since(start)), humanize.Bytes(image.GetSizeBytes()))
	}

	return nil
}

func newInstanceListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List instances",
		Args:    noArgs,
		Aliases: []string{"ls", "ps"},
		Example: "  dicer ps\n" +
			"  dicer ps --filter state=running --filter label=team=web\n" +
			"  dicer ps -c name,state,ip\n" +
			"  dicer ps --format '{{.Name}}\\t{{.IP}}'\n" +
			"  dicer stop $(dicer ps -q --filter state=running)\n" +
			"  dicer ps --watch",
		RunE: func(cmd *cobra.Command, _ []string) error {
			specs, _ := cmd.Flags().GetStringArray("filter")
			filters, err := parseInstanceFilters(specs)
			if err != nil {
				return usagef(cmd, "%s", err)
			}

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			list := func(ctx context.Context, w io.Writer) error {
				resp, err := client.ListInstances(ctx, &dicerdv1.ListInstancesRequest{})
				if err != nil {
					return err
				}
				instances := resp.GetInstances()

				return renderTo(cmd, w, &printableInstance{Instances: filters.apply(instances)})
			}

			if watch, _ := cmd.Flags().GetBool("watch"); watch {
				interval, _ := cmd.Flags().GetDuration("interval")
				return watchList(cmd, interval, list)
			}

			return list(cmd.Context(), cmd.OutOrStdout())
		},
	}

	addOutputFlags(cmd, true)
	cmd.Flags().Bool("wide", false, "Show every column, not just name, image, status, address and ports")
	cmd.Flags().BoolP("watch", "w", false, "Keep the list on screen, redrawn as it changes, until Ctrl+C")
	cmd.Flags().Duration("interval", 2*time.Second, "How often --watch redraws")
	cmd.MarkFlagsMutuallyExclusive("wide", "columns")
	cmd.Flags().StringArrayP("filter", "f", nil, "Show only instances that match, as KEY=VALUE (repeatable); "+
		"keys are "+strings.Join(instanceFilterKeys, ", "))
	_ = cmd.RegisterFlagCompletionFunc("filter", completeInstanceFilters)
	// Accepted for docker compatibility; every instance is listed anyway.
	cmd.Flags().BoolP("all", "a", false, "Accepted for Docker compatibility; every instance is always listed")
	_ = cmd.Flags().MarkHidden("all")

	return cmd
}

func newInstanceShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show NAME...",
		Short: "Show everything about one or more instances",
		Long: "Shows an instance's whole definition and state. With --format json or\n" +
			"yaml, prints the daemon's full record of it, raw sizes and all, for\n" +
			"scripts: an array of one object per instance.",
		Example: "  dicer inspect web\n" +
			"  dicer inspect web --format json | jq -r '.[0].ip'\n" +
			"  dicer inspect web --format '{{.IP}}'",
		Args:              oneOrMore("instance name"),
		Aliases:           []string{"get", "inspect"},
		ValidArgsFunction: complete(0, instancesIn()),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			instances := make([]*dicerdv1.Instance, 0, len(args))
			for _, name := range args {
				instance, err := client.GetInstance(cmd.Context(), &dicerdv1.GetInstanceRequest{Name: name})
				if err != nil {
					return suggest(cmd.Context(), client, instancesIn(), name, err)
				}
				instances = append(instances, instance)
			}

			return renderInstances(cmd, client, instances)
		},
	}

	addOutputFlags(cmd, false)
	cmd.Flags().StringSliceP("columns", "c", nil,
		"Show a table of just these columns instead, comma-separated and in any case")

	return cmd
}

// renderInstances shows instances in detail: as inspect lays them out for a
// table, as full records for JSON or YAML, and as 'dicer ps' rows for
// columns or a template.
func renderInstances(cmd *cobra.Command, client *dicer.Client, instances []*dicerdv1.Instance) error {
	format, _ := cmd.Flags().GetString("format")
	columns, _ := cmd.Flags().GetStringSlice("columns")

	switch {
	case len(columns) == 0 && printer.IsTable(format):
		recent := make(map[string][]*dicerdv1.Event, len(instances))
		for _, instance := range instances {
			events, err := recentEvents(cmd.Context(), client, instance.GetId())
			if err != nil {
				return err
			}
			recent[instance.GetId()] = events
		}

		return writeInstanceDetails(cmd.OutOrStdout(), instances, recent)
	case len(columns) == 0 && printer.IsStructured(format):
		return writeRecords(cmd.OutOrStdout(), format, instances)
	default:
		return render(cmd, &printableInstance{Instances: instances})
	}
}
