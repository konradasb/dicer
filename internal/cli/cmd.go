// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package cli implements the dicer command-line client. Its only local state
// is the list of known remotes; see internal/cli/remote.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/version"
)

// exitError ends the process with a status code and no message, such as exec
// relaying a guest command's exit status.
type exitError struct {
	code int
}

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// Execute runs the dicer command line and returns the process exit status.
func Execute() int {
	return exitStatus(NewCommand(), os.Stderr)
}

// exitStatus runs cmd, reports any error on stderr, and returns the process
// exit status.
func exitStatus(cmd *cobra.Command, stderr io.Writer) int {
	err := cmd.Execute()
	if err == nil {
		return 0
	}

	var exitErr *exitError
	if errors.As(err, &exitErr) {
		return exitErr.code
	}

	var usageErr *usageError
	if errors.As(err, &usageErr) {
		writeUsageError(stderr, usageErr)
		return 1
	}
	if msg, ok := unknownCommandMessage(err); ok {
		_, _ = io.WriteString(stderr, msg)
		return 1
	}

	_, _ = fmt.Fprintf(stderr, "Error: %s\n", errorMessage(err))
	return 1
}

// errorMessage is err as the CLI prints it. A daemon's error is a gRPC
// status, whose text names its code for a program: a person is shown its
// message alone, wherever in err it is wrapped.
func errorMessage(err error) string {
	var s interface {
		error
		GRPCStatus() *status.Status
	}
	if !errors.As(err, &s) {
		return err.Error()
	}

	return strings.Replace(err.Error(), s.Error(), s.GRPCStatus().Message(), 1)
}

// Command groups, as the root command's help lists them.
const (
	groupCommon     = "common"
	groupManagement = "management"
	groupSystem     = "system"
)

// NewCommand returns the root command for the dicer binary.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dicer",
		Short: "Run virtual machines from container images",
		Long:  "Dicer runs virtual machines from container images on a single host.",
		Example: "  dicer run -d --name web -p 8080:80 nginx:1.27\n" +
			"  dicer ps\n" +
			"  dicer exec web -- nginx -t\n" +
			"  dicer logs -f web\n" +
			"  dicer rm -f web",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version.Version,
	}

	cmd.SetVersionTemplate(version.String() + "\n")
	cmd.PersistentPreRunE = validateGlobalFlags
	cmd.SetFlagErrorFunc(flagError)
	addGlobalFlags(cmd)

	cmd.AddGroup(
		&cobra.Group{ID: groupCommon, Title: "Common Commands:"},
		&cobra.Group{ID: groupManagement, Title: "Management Commands:"},
		&cobra.Group{ID: groupSystem, Title: "System Commands:"},
	)
	cmd.SetHelpCommandGroupID(groupSystem)
	cmd.SetCompletionCommandGroupID(groupSystem)

	inGroup(groupCommon, cmd,
		shortcut(newInstanceRunCommand(), "run"),
		shortcut(newInstanceListCommand(), "ps"),
		shortcut(newInstanceExecCommand(), "exec"),
		shortcut(newInstanceLogsCommand(), "logs"),
		shortcut(newInstanceStartCommand(), "start"),
		shortcut(newInstanceStopCommand(), "stop"),
		shortcut(newInstanceRestartCommand(), "restart"),
		shortcut(newInstancePauseCommand(), "pause"),
		shortcut(newInstanceResumeCommand(), "resume", "unpause"),
		shortcut(newInstanceDeleteCommand(), "rm"),
		shortcut(newInstanceCreateCommand(), "create"),
		shortcut(newInstanceUpdateCommand(), "update"),
		shortcut(newInstanceRenameCommand(), "rename"),
		shortcut(newInstanceWaitCommand(), "wait"),
		shortcut(newInstanceShowCommand(), "inspect"),
		shortcut(newInstanceCopyCommand(), "cp"),
		shortcut(newImagePullCommand(), "pull"),
		shortcut(newImageListCommand(), "images"),
		shortcut(newImageDeleteCommand(), "rmi"),
	)
	inGroup(groupManagement, cmd,
		newInstanceCommand(),
		newImageCommand(),
		newNetworkCommand(),
		newVolumeCommand(),
		newKernelCommand(),
		newRemoteCommand(),
	)
	inGroup(groupSystem, cmd,
		newInfoCommand(),
		newEventsCommand(),
		newVersionCommand(),
	)

	forEachGroup(cmd, func(group *cobra.Command) {
		group.Args = cobra.ArbitraryArgs
		group.RunE = runGroup
	})

	return cmd
}

// forEachGroup calls fn for every command below root that only holds
// others: 'dicer instance', 'dicer instance snapshot'.
func forEachGroup(root *cobra.Command, fn func(*cobra.Command)) {
	for _, c := range root.Commands() {
		if c.HasSubCommands() {
			if c.Run == nil && c.RunE == nil {
				fn(c)
			}
			forEachGroup(c, fn)
		}
	}
}

// runGroup runs a command that only groups others: it shows help with no
// arguments and fails on an unknown subcommand.
func runGroup(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}

	var names []string
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() {
			names = append(names, c.Name())
		}
	}

	msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
	if suggestions := closeNames(args[0], names); len(suggestions) > 0 {
		msg += "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t") + "\n"
	}
	return errors.New(msg)
}

// shortcut copies a subcommand to the top level under a new name, as
// 'dicer ps' for 'dicer instance list', with only the given aliases.
func shortcut(cmd *cobra.Command, name string, aliases ...string) *cobra.Command {
	_, rest, _ := strings.Cut(cmd.Use, " ")
	cmd.Use = strings.TrimSpace(name + " " + rest)
	cmd.Aliases = aliases
	return cmd
}

// inGroup adds commands to parent under a help group.
func inGroup(group string, parent *cobra.Command, cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.GroupID = group
	}
	parent.AddCommand(cmds...)
}
