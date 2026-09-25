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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

type printableInstance struct {
	Instances []*dicerdv1.Instance
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
		state := stateName(inst.GetState())
		if e := inst.GetStateError(); e != "" {
			state += " (" + firstLine(e) + ")"
		}

		kv = append(kv, map[string]any{
			"Name":    inst.GetName(),
			"Image":   inst.GetImageRef(),
			"State":   state,
			"Status":  instanceStatus(inst),
			"VCPU":    inst.GetVcpus(),
			"Memory":  size(inst.GetMemoryBytes()),
			"Disk":    size(inst.GetDiskBytes()),
			"Network": inst.GetNetworkName(),
			"IP":      orDash(inst.GetIp()),
			"Ports":   orDash(formatPorts(inst.GetPorts())),
			"Created": age(timeOf(inst.GetCreateTime())),
		})
	}
	return kv
}

// instanceStatus describes an instance's state as docker ps does, with how
// long it has been up or since it ended, and its health if it is checked:
// "Up 3 minutes (healthy)", "Exited (1) 2 minutes ago", "Restarting (3) in 8
// seconds", "Failed: no such kernel".
func instanceStatus(inst *dicerdv1.Instance) string {
	switch state := inst.GetState(); state {
	case stateRunning:
		if started := timeOf(inst.GetStartTime()); !started.IsZero() {
			return "Up " + units.HumanDuration(time.Since(started)) + healthSuffix(inst)
		}

		return "Up" + healthSuffix(inst)
	case stateRestarting:
		out := fmt.Sprintf("Restarting (%d)", inst.GetRestartCount())
		if next := timeOf(inst.GetNextRestartTime()); !next.IsZero() && time.Until(next) >= time.Second {
			out += " in " + units.HumanDuration(time.Until(next))
		}

		return out
	case stateStopped, stateFailed:
		// An instance whose workload exited says so, and when, as a
		// container would.
		if finished := timeOf(inst.GetFinishTime()); inst.ExitCode != nil && !finished.IsZero() {
			return fmt.Sprintf("Exited (%d) %s", inst.GetExitCode(), age(finished))
		}
		if e := inst.GetStateError(); e != "" && state == stateFailed {
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

			inst, err := client.UpdateInstance(cmd.Context(), req)
			if err != nil {
				return suggest(cmd.Context(), client, instancesIn(), req.GetName(), err)
			}

			succeeded(cmd, "Instance %s updated", inst.GetName())

			return nil
		},
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().StringP("image", "i", "", "Container image reference")
	_ = cmd.RegisterFlagCompletionFunc("image", complete(0, listImages))
	addInstanceSpecFlags(cmd, false)

	return cmd
}

// createInstance sends a create request and reports the result. For a start,
// the image is pulled first so its progress shows.
func createInstance(cmd *cobra.Command, req *dicerdv1.CreateInstanceRequest) error {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	if !req.GetStart() {
		inst, err := client.CreateInstance(cmd.Context(), req)
		if err != nil {
			return err
		}
		succeeded(cmd, "Instance %s created. Start it with: dicer start %s", inst.GetName(), inst.GetName())

		return nil
	}

	if err := ensureImage(cmd, client, req.GetImageRef()); err != nil {
		return err
	}

	err = runTask(cmd, "Starting "+req.GetName(), func() (*dicerdv1.Instance, error) {
		return client.CreateInstance(cmd.Context(), req)
	}, func(inst *dicerdv1.Instance, took string) string {
		return fmt.Sprintf("Instance %s started in %s (%s)", inst.GetName(), took, orDash(inst.GetIp()))
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

// ensureImage pulls an image the host does not hold, showing progress.
func ensureImage(cmd *cobra.Command, client *dicer.Client, ref string) error {
	_, err := client.GetImage(cmd.Context(), &dicerdv1.GetImageRequest{Ref: ref})
	if err == nil {
		return nil
	}
	if status.Code(err) != codes.NotFound {
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
	img, err := pullImage(cmd.Context(), client, ref, reporter.report)
	if err != nil {
		return err
	}
	reporter.done()

	if reporter.fetched {
		succeeded(cmd, "Image %s pulled in %s (%s)",
			img.GetName(), formatDuration(time.Since(start)), size(img.GetSizeBytes()))
	}

	return nil
}

// osCause strips the operation and path from a file error.
func osCause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
