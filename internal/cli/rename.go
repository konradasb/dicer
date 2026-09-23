// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"github.com/spf13/cobra"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func newInstanceRenameCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rename NAME NEW_NAME",
		Short: "Rename a stopped instance",
		Long: "Renames a stopped instance. It keeps its ID, its disks, its snapshots and\n" +
			"the address it holds: only what you call it changes.\n\n" +
			"A running instance is refused. Its name is where its files are kept on the\n" +
			"host, and its guest took its hostname from the old name when it booted, so\n" +
			"a rename could not fully take effect until it is started again.",
		Example:           "  dicer rename web web-old\n  dicer instance rename api api-v2",
		Args:              needs([]string{"an instance name", "a new name"}),
		ValidArgsFunction: complete(1, instancesIn()),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, to := args[0], args[1]

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			inst, err := client.RenameInstance(cmd.Context(), &dicerdv1.RenameInstanceRequest{Name: from, NewName: to})
			if err != nil {
				return suggest(cmd.Context(), client, instancesIn(), from, err)
			}

			succeeded(cmd, "Instance %s renamed to %s", from, inst.GetName())

			return nil
		},
	}
}
