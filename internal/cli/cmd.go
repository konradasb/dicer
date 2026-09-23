// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package cli implements the dicer command-line client.
//
// It talks to a daemon -- the local one over its Unix socket, or a remote one
// over mutual TLS -- and holds no state of its own beyond which daemons it
// knows: everything it shows comes from a single RPC. See client.go and
// internal/cli/remote.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
)

// exitError ends the process with a status code and no message of its own,
// for a command that has already said everything it needs to -- exec
// relaying a guest command's exit status, for one.
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

	_, _ = fmt.Fprintf(stderr, "Error: %s\n", err)
	return 1
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
		Example: "  dicer run --name web -p 8080:80 nginx:1.27\n" +
			"  dicer ps\n" +
			"  dicer exec web -- nginx -t\n" +
			"  dicer logs -f web\n" +
			"  dicer rm -f web",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       dicer.Version,
	}

	cmd.SetVersionTemplate(dicer.VersionString() + "\n")
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
		newClientCommand(),
		newTokenCommand(),
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

// runGroup runs a command that only holds others: given nothing, it shows
// what they are; given anything, that it is not one of them. Cobra would
// show the help either way, and exit 0 on a typo.
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

// shortcut renames a subcommand to stand at the top level, as 'dicer ps'
// stands for 'dicer instance list'. Its own aliases would crowd the top
// level, so it takes only those given.
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
