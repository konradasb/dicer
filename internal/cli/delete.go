// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
)

// addDeleteAllFlags adds --all, and --yes to go with it, to a command that
// deletes things of a kind, plural.
func addDeleteAllFlags(cmd *cobra.Command, plural string) {
	cmd.Flags().BoolP("all", "A", false, "Delete all "+plural)
	cmd.Flags().BoolP("yes", "y", false, "With --all, do not ask before deleting")
}

// namesOrAll accepts one or more names, or none with --all.
func namesOrAll(what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if all, _ := cmd.Flags().GetBool("all"); all {
			if len(args) > 0 {
				return usagef(cmd, "%s takes names or --all, not both", cmd.CommandPath())
			}
			return nil
		}
		return oneOrMore(what)(cmd, args)
	}
}

// eachNameOrAll runs do for each name given, as eachName does, or with
// --all for every one list returns, once the deleting has been confirmed.
func eachNameOrAll(
	cmd *cobra.Command, args []string, list completer, plural string,
	do func(client *dicer.Client, name string) error,
) error {
	if all, _ := cmd.Flags().GetBool("all"); !all {
		return eachName(cmd, args, list, do)
	}

	names, err := allNames(cmd, list)
	if err != nil {
		return err
	}
	if ok, err := confirmDeleteAll(cmd, plural, names); err != nil || !ok {
		return err
	}
	return eachName(cmd, names, list, do)
}

// allNames returns the names list offers, without their descriptions.
func allNames(cmd *cobra.Command, list completer) ([]string, error) {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	names, err := list(cmd.Context(), client, nil)
	if err != nil {
		return nil, err
	}

	for i, n := range names {
		names[i] = completionValue(n)
	}
	return names, nil
}

// confirmDeleteAll asks before deleting all of names, unless --yes says not
// to, and reports whether to go ahead. With nothing to delete, it says so
// and does not.
func confirmDeleteAll(cmd *cobra.Command, plural string, names []string) (bool, error) {
	if len(names) == 0 {
		succeeded(cmd, "There are no %s to delete", plural)
		return false, nil
	}
	if yes, _ := cmd.Flags().GetBool("yes"); yes {
		return true, nil
	}
	return confirm(cmd, fmt.Sprintf("Delete all %d %s: %s?", len(names), plural, strings.Join(names, ", ")))
}
