// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/cli/printer"
)

type printableVolume struct {
	Volumes []dicer.Volume
}

func (p *printableVolume) Cols() []string {
	return []string{"ID", "Name", "Size", "Created"}
}

func (p *printableVolume) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Volumes))
	for _, v := range p.Volumes {
		kv = append(kv, map[string]any{
			"ID":      v.ID,
			"Name":    v.Name,
			"Size":    size(v.SizeBytes),
			"Created": age(v.CreatedAt),
		})
	}
	return kv
}

func newVolumeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volume",
		Short:   "Manage volumes",
		Aliases: []string{"volumes"},
	}

	cmd.AddCommand(
		newVolumeCreateCommand(),
		newVolumeListCommand(),
		newVolumeShowCommand(),
		newVolumeDeleteCommand(),
	)

	return cmd
}

func newVolumeCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "create NAME",
		Short:   "Create a volume",
		Args:    one("a name for the volume"),
		Aliases: []string{"new"},
		RunE: func(cmd *cobra.Command, args []string) error {
			sizeFlag, _ := cmd.Flags().GetString("size")
			sizeBytes, err := units.RAMInBytes(sizeFlag)
			if err != nil {
				return fmt.Errorf("invalid volume size %q: want a size like 10GiB", sizeFlag)
			}

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			v, err := client.CreateVolume(cmd.Context(), args[0], sizeBytes)
			if err != nil {
				return err
			}

			succeeded(cmd, "Volume %s created (%s)", v.Name, size(v.SizeBytes))
			return nil
		},
	}

	cmd.Flags().String("size", "", "Volume size, e.g. 10GiB")
	requireFlag(cmd, "size", "10GiB")

	return cmd
}

func newVolumeListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List volumes",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			volumes, err := client.ListVolumes(cmd.Context())
			if err != nil {
				return err
			}

			return render(cmd, &printableVolume{Volumes: volumes})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newVolumeShowCommand() *cobra.Command {
	return newShowCommand(showSpec[dicer.Volume]{
		use:       "show NAME",
		short:     "Show a volume",
		arg:       "a volume name",
		list:      listVolumes,
		get:       (*dicer.Client).GetVolume,
		printable: func(v dicer.Volume) printer.Printable { return &printableVolume{Volumes: []dicer.Volume{v}} },
	})
}

func newVolumeDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "delete NAME...",
		Short:             "Delete one or more volumes no instance uses",
		Args:              oneOrMore("volume name"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: complete(0, listVolumes),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachName(cmd, args, listVolumes, func(client *dicer.Client, name string) error {
				if err := client.DeleteVolume(cmd.Context(), name); err != nil {
					return err
				}

				succeeded(cmd, "Volume %s deleted", name)
				return nil
			})
		},
	}
}
