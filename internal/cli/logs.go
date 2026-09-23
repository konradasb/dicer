// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
)

func newInstanceLogsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Show an instance's console output",
		Long: "Shows the guest's serial console: the kernel's boot messages, dicer-init's,\n" +
			"and whatever the workload writes to the console. It is kept with the\n" +
			"instance, so it can be read after a stop to explain one.\n\n" +
			"Use --source hypervisor for the VMM's own log, which explains a guest that\n" +
			"never got as far as booting. That one is discarded when the instance stops.",
		Example: "  dicer logs -f web\n" +
			"  dicer logs -n 50 web\n" +
			"  dicer logs --source hypervisor web",
		Args:              one("an instance name"),
		RunE:              runInstanceLogsCommand,
		ValidArgsFunction: complete(1, instancesIn()),
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().BoolP("follow", "f", false, "Keep writing new output until the instance stops")
	cmd.Flags().Int32P("tail", "n", 0, "Show only the last lines (default: all)")
	cmd.Flags().String("source", "guest", "Which log to read: guest or hypervisor")
	_ = cmd.RegisterFlagCompletionFunc("source", fixedCompletions(
		"guest\tThe guest's serial console", "hypervisor\tThe VMM's own log"))

	return cmd
}

func runInstanceLogsCommand(cmd *cobra.Command, args []string) error {
	follow, _ := cmd.Flags().GetBool("follow")
	tail, _ := cmd.Flags().GetInt32("tail")
	sourceFlag, _ := cmd.Flags().GetString("source")

	source, err := logSource(sourceFlag)
	if err != nil {
		return usagef(cmd, "%s", err)
	}

	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	err = client.InstanceLogs(cmd.Context(), args[0], dicer.LogOptions{
		Source:    source,
		TailLines: int(tail),
		Follow:    follow,
	}, cmd.OutOrStdout())
	if err != nil {
		return suggest(cmd.Context(), client, instancesIn(), args[0], err)
	}

	return nil
}

// logSource maps the --source flag onto the log it names.
func logSource(flag string) (dicer.LogSource, error) {
	switch flag {
	case "guest":
		return dicer.LogSourceGuest, nil
	case "hypervisor", "vmm":
		return dicer.LogSourceHypervisor, nil
	default:
		return "", fmt.Errorf("invalid --source %q: want guest or hypervisor", flag)
	}
}
