// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

type printableSnapshot struct {
	Snapshots []*dicerdv1.Snapshot
}

func (p *printableSnapshot) Cols() []string {
	return []string{"Name", "Instance", "Hypervisor", "Memory", "Size", "Created"}
}

func (p *printableSnapshot) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Snapshots))
	for _, s := range p.Snapshots {
		kv = append(kv, map[string]any{
			"Name":       s.GetName(),
			"Instance":   s.GetInstanceName(),
			"Hypervisor": enumName(s.GetHypervisorType()) + " " + s.GetHypervisorVersion(),
			"Memory":     size(s.GetMemoryBytes()),
			"Size":       size(s.GetSizeBytes()),
			"Created":    age(timeOf(s.GetCreateTime())),
		})
	}
	return kv
}

func newInstanceSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Freeze instances to disk and put them back",
		Long: "A snapshot holds a guest's memory, its device state and a copy of its\n" +
			"overlay disk, so restoring resumes it exactly where it was.",
		Aliases: []string{"snapshots", "snap"},
	}

	cmd.AddCommand(
		newSnapshotCreateCommand(),
		newSnapshotListCommand(),
		newSnapshotShowCommand(),
		newSnapshotRestoreCommand(),
		newSnapshotDeleteCommand(),
	)

	return cmd
}

func newSnapshotCreateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "create INSTANCE [NAME]",
		Short: "Snapshot a running or paused instance",
		Long: "Snapshots a running or paused instance. A running one is paused for as\n" +
			"long as it takes and resumed afterwards. The name defaults to the time\n" +
			"the snapshot is taken.",
		Args:              needs([]string{"an instance name"}, "a name for the snapshot"),
		Aliases:           []string{"new"},
		ValidArgsFunction: complete(1, instancesIn(stateRunning, statePaused)),
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
			}, func(snap *dicerdv1.Snapshot, took string) string {
				return fmt.Sprintf("Snapshot %s of instance %s created in %s (%s)",
					snap.GetName(), snap.GetInstanceName(), took, size(snap.GetSizeBytes()))
			})
		},
	}
}

func newSnapshotListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list INSTANCE",
		Short:             "List an instance's snapshots",
		Args:              one("an instance name"),
		Aliases:           []string{"ls"},
		ValidArgsFunction: complete(1, instancesIn()),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			resp, err := client.ListSnapshots(cmd.Context(), &dicerdv1.ListSnapshotsRequest{Instance: args[0]})
			if err != nil {
				return err
			}

			return render(cmd, &printableSnapshot{Snapshots: resp.GetSnapshots()})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newSnapshotShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "show INSTANCE NAME",
		Short:             "Show a snapshot",
		Args:              needs([]string{"an instance name", "a snapshot name"}),
		Aliases:           []string{"get", "inspect"},
		ValidArgsFunction: completeSnapshotArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			snap, err := client.GetSnapshot(cmd.Context(), &dicerdv1.GetSnapshotRequest{Instance: args[0], Name: args[1]})
			if err != nil {
				return err
			}

			return render(cmd, &printableSnapshot{Snapshots: []*dicerdv1.Snapshot{snap}})
		},
	}

	addOutputFlags(cmd, false)

	return cmd
}

func newSnapshotRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore INSTANCE NAME",
		Short: "Restore a stopped instance from a snapshot and resume it",
		Long: "Puts a stopped instance back to the moment the snapshot was taken and\n" +
			"resumes it. Anything written to its disk since is discarded.",
		Args:              needs([]string{"an instance name", "a snapshot name"}),
		ValidArgsFunction: completeSnapshotArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runTask(cmd, "Restoring "+args[0], func() (*dicerdv1.Instance, error) {
				return client.RestoreSnapshot(cmd.Context(), &dicerdv1.RestoreSnapshotRequest{Instance: args[0], Name: args[1]})
			}, func(inst *dicerdv1.Instance, took string) string {
				return fmt.Sprintf("Instance %s restored from snapshot %s in %s (%s)",
					inst.GetName(), args[1], took, orDash(inst.GetIp()))
			})
		},
	}
}

func newSnapshotDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "delete INSTANCE NAME",
		Short:             "Delete a snapshot",
		Args:              needs([]string{"an instance name", "a snapshot name"}),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: completeSnapshotArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			if _, err := client.DeleteSnapshot(cmd.Context(), &dicerdv1.DeleteSnapshotRequest{Instance: args[0], Name: args[1]}); err != nil {
				return err
			}

			succeeded(cmd, "Snapshot %s of instance %s deleted", args[1], args[0])
			return nil
		},
	}
}
