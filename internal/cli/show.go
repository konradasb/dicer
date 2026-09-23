// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
)

// showSpec describes a command that shows one resource by name.
type showSpec[T any] struct {
	use   string    // e.g. "show NAME"
	short string    // e.g. "Show a kernel"
	arg   string    // the argument, as a usage error names it
	list  completer // offers names to complete

	// get fetches the named resource.
	get func(ctx context.Context, client *dicer.Client, name string) (T, error)

	// printable wraps a fetched resource for printing.
	printable func(T) printer.Printable
}

// newShowCommand returns the command spec describes.
func newShowCommand[T any](spec showSpec[T]) *cobra.Command {
	cmd := &cobra.Command{
		Use:               spec.use,
		Short:             spec.short,
		Args:              one(spec.arg),
		Aliases:           []string{"get", "inspect"},
		ValidArgsFunction: complete(1, spec.list),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			v, err := spec.get(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}

			return render(cmd, spec.printable(v))
		},
	}

	addOutputFlags(cmd, false)

	return cmd
}
