// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// eachName runs do for each name, reporting failures as they happen and
// failing at the end. A name not found gets a suggestion from list.
func eachName(
	cmd *cobra.Command, names []string, list completer,
	do func(client *dicer.Client, name string) error,
) error {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	run := func(name string) error {
		return suggest(cmd.Context(), client, list, name, do(client, name))
	}

	if len(names) == 1 {
		return run(names[0])
	}

	failed := false
	for _, name := range names {
		if err := run(name); err != nil {
			failed = true
			cmd.PrintErrf("Error: %s\n", err)
		}
	}
	if failed {
		return &exitError{code: 1}
	}
	return nil
}

// runTask runs a slow call, showing a spinner while it lasts, and says how
// it went: "Instance web started in 1.4s (172.20.0.7)".
func runTask[T any](cmd *cobra.Command, doing string, call func() (T, error), done func(T, string) string) error {
	t := startTask(cmd, doing)
	result, err := call()
	if err != nil {
		t.end()
		return err
	}

	t.succeed("%s", done(result, formatDuration(t.elapsed())))
	return nil
}

func newInstanceStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "start NAME...",
		Short:             "Start one or more defined instances",
		Args:              oneOrMore("instance name"),
		ValidArgsFunction: complete(0, instancesIn(stateStopped, stateFailed, stateRestarting)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachName(cmd, args, instancesIn(), func(client *dicer.Client, name string) error {
				return runTask(cmd, "Starting "+name, func() (*dicerdv1.Instance, error) {
					return client.StartInstance(cmd.Context(), &dicerdv1.StartInstanceRequest{Name: name})
				}, func(inst *dicerdv1.Instance, took string) string {
					return fmt.Sprintf("Instance %s started in %s (%s)",
						inst.GetName(), took, orDash(inst.GetIp()))
				})
			})
		},
	}
}

func newInstanceStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "stop NAME...",
		Short:             "Stop one or more running instances, keeping their definitions and disks",
		Args:              oneOrMore("instance name"),
		ValidArgsFunction: complete(0, instancesIn(stateRunning, statePaused, stateStarting, stateRestarting)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachName(cmd, args, instancesIn(), func(client *dicer.Client, name string) error {
				return runTask(cmd, "Stopping "+name, func() (*dicerdv1.Instance, error) {
					return client.StopInstance(cmd.Context(), &dicerdv1.StopInstanceRequest{Name: name})
				}, func(inst *dicerdv1.Instance, took string) string {
					return fmt.Sprintf("Instance %s stopped in %s", inst.GetName(), took)
				})
			})
		},
	}
}

func newInstanceRestartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart NAME...",
		Short: "Stop one or more instances if they are running, then start them",
		Long: "Stops each instance if it is running or paused, then starts it. A stopped\n" +
			"instance is just started. Restarting is how a changed file mount or an\n" +
			"updated image takes effect.",
		Args:              oneOrMore("instance name"),
		ValidArgsFunction: complete(0, instancesIn()),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachName(cmd, args, instancesIn(), func(client *dicer.Client, name string) error {
				return runTask(cmd, "Restarting "+name, func() (*dicerdv1.Instance, error) {
					inst, err := client.GetInstance(cmd.Context(), &dicerdv1.GetInstanceRequest{Name: name})
					if err != nil {
						return nil, err
					}

					switch inst.GetState() {
					case stateRunning, statePaused, stateStarting:
						if _, err := client.StopInstance(cmd.Context(), &dicerdv1.StopInstanceRequest{Name: name}); err != nil {
							return nil, err
						}
					}

					return client.StartInstance(cmd.Context(), &dicerdv1.StartInstanceRequest{Name: name})
				}, func(inst *dicerdv1.Instance, took string) string {
					return fmt.Sprintf("Instance %s restarted in %s (%s)",
						inst.GetName(), took, orDash(inst.GetIp()))
				})
			})
		},
	}
}

func newInstancePauseCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "pause NAME...",
		Short:             "Pause one or more running instances",
		Args:              oneOrMore("instance name"),
		ValidArgsFunction: complete(0, instancesIn(stateRunning)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachName(cmd, args, instancesIn(), func(client *dicer.Client, name string) error {
				inst, err := client.PauseInstance(cmd.Context(), &dicerdv1.PauseInstanceRequest{Name: name})
				if err != nil {
					return err
				}

				succeeded(cmd, "Instance %s paused", inst.GetName())

				return nil
			})
		},
	}
}

func newInstanceResumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "resume NAME...",
		Short:             "Resume one or more paused instances",
		Aliases:           []string{"unpause"},
		Args:              oneOrMore("instance name"),
		ValidArgsFunction: complete(0, instancesIn(statePaused)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachName(cmd, args, instancesIn(), func(client *dicer.Client, name string) error {
				inst, err := client.ResumeInstance(cmd.Context(), &dicerdv1.ResumeInstanceRequest{Name: name})
				if err != nil {
					return err
				}

				succeeded(cmd, "Instance %s resumed", inst.GetName())

				return nil
			})
		},
	}
}

func newInstanceDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "delete NAME...",
		Short:             "Delete one or more instances",
		Args:              oneOrMore("instance name"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: complete(0, instancesIn()),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")

			return eachName(cmd, args, instancesIn(), func(client *dicer.Client, name string) error {
				_, err := client.DeleteInstance(cmd.Context(), &dicerdv1.DeleteInstanceRequest{Name: name, Force: force})
				if err != nil {
					return withHint(err, codes.FailedPrecondition, "stop it first or use -f")
				}

				succeeded(cmd, "Instance %s deleted", name)
				return nil
			})
		},
	}

	cmd.Flags().BoolP("force", "f", false, "Stop an instance first if it is running")

	return cmd
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
				inst, err := client.GetInstance(cmd.Context(), &dicerdv1.GetInstanceRequest{Name: name})
				if err != nil {
					return suggest(cmd.Context(), client, instancesIn(), name, err)
				}
				instances = append(instances, inst)
			}

			return renderInstances(cmd, client, instances)
		},
	}

	addOutputFlags(cmd, false)
	cmd.Flags().StringSliceP("columns", "c", nil,
		"Show a table of just these columns instead, comma-separated and in any case")

	return cmd
}

// renderInstances shows instances in detail, as full records for JSON or
// YAML, or as 'dicer ps' rows for columns or a template.
func renderInstances(cmd *cobra.Command, client *dicer.Client, instances []*dicerdv1.Instance) error {
	format, _ := cmd.Flags().GetString("format")
	columns, _ := cmd.Flags().GetStringSlice("columns")

	switch {
	case len(columns) > 0 || strings.Contains(format, "{{"):
		return render(cmd, &printableInstance{Instances: instances})
	case strings.EqualFold(format, "table") || strings.EqualFold(format, "text"):
		recent := make(map[string][]*dicerdv1.Event, len(instances))
		for _, inst := range instances {
			events, err := recentEvents(cmd.Context(), client, inst.GetId())
			if err != nil {
				return err
			}
			recent[inst.GetId()] = events
		}

		return writeInstanceDetails(cmd.OutOrStdout(), instances, recent)
	default:
		return writeRecords(cmd.OutOrStdout(), format, instances)
	}
}
