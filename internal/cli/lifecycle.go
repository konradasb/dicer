// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

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
			cmd.PrintErrf("Error: %s\n", errorMessage(err))
		}
	}
	if failed {
		return &exitError{code: 1}
	}
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
				}, func(instance *dicerdv1.Instance, took string) string {
					return fmt.Sprintf("Instance %s started in %s (%s)",
						instance.GetName(), took, orDash(instance.GetIp()))
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
				}, func(instance *dicerdv1.Instance, took string) string {
					return fmt.Sprintf("Instance %s stopped in %s", instance.GetName(), took)
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
					instance, err := client.GetInstance(cmd.Context(), &dicerdv1.GetInstanceRequest{Name: name})
					if err != nil {
						return nil, err
					}

					switch instance.GetState() {
					case stateRunning, statePaused, stateStarting:
						if _, err := client.StopInstance(cmd.Context(), &dicerdv1.StopInstanceRequest{Name: name}); err != nil {
							return nil, err
						}
					}

					return client.StartInstance(cmd.Context(), &dicerdv1.StartInstanceRequest{Name: name})
				}, func(instance *dicerdv1.Instance, took string) string {
					return fmt.Sprintf("Instance %s restarted in %s (%s)",
						instance.GetName(), took, orDash(instance.GetIp()))
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
				instance, err := client.PauseInstance(cmd.Context(), &dicerdv1.PauseInstanceRequest{Name: name})
				if err != nil {
					return err
				}

				succeeded(cmd, "Instance %s paused", instance.GetName())

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
				instance, err := client.ResumeInstance(cmd.Context(), &dicerdv1.ResumeInstanceRequest{Name: name})
				if err != nil {
					return err
				}

				succeeded(cmd, "Instance %s resumed", instance.GetName())

				return nil
			})
		},
	}
}

func newInstanceDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete (NAME... | --all)",
		Short: "Delete one or more instances, or all of them",
		Long: "Deletes the instances named, or with --all every instance, asking first on a\n" +
			"terminal. A running instance is refused unless -f stops it first.",
		Example: "  dicer rm web\n" +
			"  dicer rm -f web worker\n" +
			"  dicer rm --all -f",
		Args:              namesOrAll("instance name"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: complete(0, instancesIn()),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")

			return eachNameOrAll(cmd, args, instancesIn(), "instances", func(client *dicer.Client, name string) error {
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
	addDeleteAllFlags(cmd, "instances")

	return cmd
}
