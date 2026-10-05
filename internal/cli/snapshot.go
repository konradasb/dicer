// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
	"github.com/konradasb/dicer/internal/humanize"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

type printableSnapshot struct {
	Snapshots []*dicerdv1.Snapshot
}

func (p *printableSnapshot) Columns() []string {
	return []string{"Name", "Kind", "Instance", "Hypervisor", "Memory", "Size", "Created"}
}

func (p *printableSnapshot) Rows() []map[string]any {
	rows := make([]map[string]any, 0, len(p.Snapshots))
	for _, s := range p.Snapshots {
		hypervisor, memory := "-", "-"
		if s.GetKind() == dicerdv1.SnapshotKind_SNAPSHOT_KIND_MEMORY {
			hypervisor = enumName(s.GetHypervisorType()) + " " + s.GetHypervisorVersion()
			memory = humanize.Bytes(s.GetMemoryBytes())
		}
		rows = append(rows, map[string]any{
			"Name":       s.GetName(),
			"Kind":       enumName(s.GetKind()),
			"Instance":   s.GetInstanceName(),
			"Hypervisor": hypervisor,
			"Memory":     memory,
			"Size":       humanize.Bytes(s.GetSizeBytes()),
			"Created":    age(timeOf(s.GetCreateTime())),
		})
	}
	return rows
}

func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage snapshots",
		Long: "A snapshot freezes an instance to disk. A memory snapshot, of a running or\n" +
			"paused instance, holds its guest's memory, device state and overlay disk, so\n" +
			"restoring it resumes the guest where it was. A disk snapshot, of a stopped\n" +
			"instance, holds its overlay disk alone. Neither holds volumes. A snapshot\n" +
			"outlives the instance it was taken from.",
		Aliases: []string{"snapshots", "snap"},
	}

	cmd.AddCommand(
		newSnapshotCreateCommand(),
		newSnapshotListCommand(),
		newSnapshotShowCommand(),
		newSnapshotRestoreCommand(),
		newSnapshotForkCommand(),
		newSnapshotDeleteCommand(),
	)

	return cmd
}

func newSnapshotCreateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "create INSTANCE [NAME]",
		Short: "Snapshot an instance",
		Long: "Snapshots an instance: a running or paused one's memory and disk, pausing a\n" +
			"running one for as long as it takes, or a stopped one's disk. The name\n" +
			"defaults to the instance's and the time's.",
		Args:              needs([]string{"an instance name"}, "a name for the snapshot"),
		Aliases:           []string{"new"},
		ValidArgsFunction: complete(1, instancesIn()),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			var name string
			if len(args) > 1 {
				name = args[1]
			}

			return runTask(cmd, "Snapshotting "+args[0], func() (*dicerdv1.Snapshot, error) {
				return client.CreateSnapshot(cmd.Context(), &dicerdv1.CreateSnapshotRequest{Instance: args[0], Name: name})
			}, func(snapshot *dicerdv1.Snapshot, took string) string {
				return fmt.Sprintf("Snapshot %s of instance %s created in %s (%s, %s)", snapshot.GetName(),
					snapshot.GetInstanceName(), took, enumName(snapshot.GetKind()), humanize.Bytes(snapshot.GetSizeBytes()))
			})
		},
	}
}

func newSnapshotListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List snapshots",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			instance, _ := cmd.Flags().GetString("instance")
			resp, err := client.ListSnapshots(cmd.Context(), &dicerdv1.ListSnapshotsRequest{Instance: instance})
			if err != nil {
				return err
			}

			return render(cmd, &printableSnapshot{Snapshots: resp.GetSnapshots()})
		},
	}

	cmd.Flags().String("instance", "", "List only the snapshots of this instance")
	_ = cmd.RegisterFlagCompletionFunc("instance", complete(0, instancesIn()))
	addOutputFlags(cmd, true)

	return cmd
}

func newSnapshotShowCommand() *cobra.Command {
	return newShowCommand(showSpec[*dicerdv1.Snapshot]{
		use:   "show NAME",
		short: "Show a snapshot",
		arg:   "a snapshot name",
		list:  listSnapshots,
		get: func(ctx context.Context, client *dicer.Client, name string) (*dicerdv1.Snapshot, error) {
			return client.GetSnapshot(ctx, &dicerdv1.GetSnapshotRequest{Name: name})
		},
		printable: func(s *dicerdv1.Snapshot) printer.Printable {
			return &printableSnapshot{Snapshots: []*dicerdv1.Snapshot{s}}
		},
	})
}

func newSnapshotRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore NAME",
		Short: "Put an instance back as a snapshot of it holds it",
		Long: "Puts the stopped instance a snapshot was taken of back as it was then,\n" +
			"discarding whatever it has written to its disk since. A memory snapshot\n" +
			"resumes the guest where it was; a disk snapshot leaves the instance stopped,\n" +
			"to boot from the restored disk at its next start.",
		Args:              one("a snapshot name"),
		ValidArgsFunction: complete(1, listSnapshots),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runTask(cmd, "Restoring "+args[0], func() (*dicerdv1.Instance, error) {
				return client.RestoreSnapshot(cmd.Context(), &dicerdv1.RestoreSnapshotRequest{Name: args[0]})
			}, func(instance *dicerdv1.Instance, took string) string {
				if instance.GetState() != dicerdv1.InstanceState_INSTANCE_STATE_RUNNING {
					return fmt.Sprintf("Disk of instance %s restored from snapshot %s in %s; start it to boot from it",
						instance.GetName(), args[0], took)
				}
				return fmt.Sprintf("Instance %s restored from snapshot %s in %s (%s)",
					instance.GetName(), args[0], took, orDash(instance.GetIp()))
			})
		},
	}
}

func newSnapshotForkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fork SNAPSHOT NAME",
		Short: "Create an instance as a copy of a snapshot's",
		Long: "Creates an instance called NAME as a copy of the one a snapshot was taken of,\n" +
			"with its definition and disk but an address of its own, on the same network\n" +
			"unless --network is given. It publishes no ports unless -p is given: two\n" +
			"instances cannot publish the same host port.\n\n" +
			"A memory snapshot's copy runs, resumed where the snapshot's guest was and\n" +
			"given its own name and address before it can reach the network. A disk\n" +
			"snapshot's copy is stopped, to boot from the snapshot's disk.",
		Example: "  dicer snapshot fork web-golden web-2\n" +
			"  dicer snapshot fork web-golden web-3 -p 8081:80",
		Args:              needs([]string{"a snapshot name", "a name for the new instance"}),
		ValidArgsFunction: complete(1, listSnapshots),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &dicerdv1.ForkSnapshotRequest{Name: args[0], Instance: args[1]}
			req.NetworkName, _ = cmd.Flags().GetString("network")
			req.StaticIp, _ = cmd.Flags().GetString("ip")
			specs, _ := cmd.Flags().GetStringArray("publish")
			ports, err := parseEach(specs, parsePortMapping)
			if err != nil {
				return err
			}
			req.Ports = ports

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runTask(cmd, "Forking "+args[0], func() (*dicerdv1.Instance, error) {
				return client.ForkSnapshot(cmd.Context(), req)
			}, func(instance *dicerdv1.Instance, took string) string {
				if instance.GetState() != dicerdv1.InstanceState_INSTANCE_STATE_RUNNING {
					return fmt.Sprintf("Instance %s forked from snapshot %s in %s; start it to boot it", instance.GetName(), args[0], took)
				}
				return fmt.Sprintf("Instance %s forked from snapshot %s in %s (%s)",
					instance.GetName(), args[0], took, orDash(instance.GetIp()))
			})
		},
	}

	flags := cmd.Flags()
	flags.String("network", "", "Network to attach to (default: the snapshot's instance's)")
	flags.String("ip", "", "Static IP address (default: assigned from the subnet)")
	flags.StringArrayP("publish", "p", nil,
		"Publish a guest port on the host, as [hostIP:]hostPort:guestPort[/tcp|udp] (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("network", complete(0, listNetworks))

	return cmd
}

func newSnapshotDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete (NAME... | --all)",
		Short: "Delete one or more snapshots, or all of them",
		Long: "Deletes the snapshots named, or with --all every snapshot, asking first on\n" +
			"a terminal.",
		Args:              namesOrAll("snapshot name"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: complete(0, listSnapshots),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachNameOrAll(cmd, args, listSnapshots, "snapshots", func(client *dicer.Client, name string) error {
				if _, err := client.DeleteSnapshot(cmd.Context(), &dicerdv1.DeleteSnapshotRequest{Name: name}); err != nil {
					return err
				}

				succeeded(cmd, "Snapshot %s deleted", name)
				return nil
			})
		},
	}
	addDeleteAllFlags(cmd, "snapshots")

	return cmd
}
