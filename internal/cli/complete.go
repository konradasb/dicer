// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/cli/remote"
)

// completionTimeout bounds how long a completion waits on the daemon. A
// shell that hangs on <Tab> is worse than one that offers nothing.
const completionTimeout = 2 * time.Second

// completer lists the names a completion offers, each with a description
// after a tab.
type completer func(ctx context.Context, client *dicer.Client, args []string) ([]string, error)

// complete turns a completer into a cobra completion function. It offers
// names for the first maxArgs arguments (every argument if maxArgs is 0),
// leaves out those already given, and quietly offers nothing if the daemon
// cannot be reached.
func complete(maxArgs int, list completer) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if maxArgs > 0 && len(args) >= maxArgs {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		client, cleanup, err := newClient(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		defer cleanup()

		ctx, cancel := context.WithTimeout(contextOf(cmd), completionTimeout)
		defer cancel()

		names, err := list(ctx, client, args)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		return slices.DeleteFunc(names, func(n string) bool {
			return slices.Contains(args, completionValue(n))
		}), cobra.ShellCompDirectiveNoFileComp
	}
}

// contextOf returns the command's context, which is unset for a command
// run outside Execute -- as a completion function can be in a test.
func contextOf(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// completionValue returns the value of a completion, without the
// description that follows it.
func completionValue(s string) string {
	value, _, _ := strings.Cut(s, "\t")
	return value
}

// instancesIn lists instances, described by state and image, keeping only
// those in one of states if any are given: 'dicer start <Tab>' offers what
// is stopped, not what is already running.
func instancesIn(states ...dicer.InstanceState) completer {
	return func(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
		instances, err := client.ListInstances(ctx)
		if err != nil {
			return nil, err
		}

		var names []string
		for _, inst := range instances {
			if len(states) > 0 && !slices.Contains(states, inst.Status.State) {
				continue
			}
			names = append(names, inst.Spec.Name+"\t"+inst.Status.State.String()+", "+inst.Spec.ImageRef)
		}

		return names, nil
	}
}

func listImages(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
	images, err := client.ListImages(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(images))
	for _, img := range images {
		names = append(names, img.Name+"\t"+size(img.SizeBytes))
	}

	return names, nil
}

func listNetworks(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
	networks, err := client.ListNetworks(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(networks))
	for _, n := range networks {
		names = append(names, n.Name+"\t"+n.Subnet)
	}

	return names, nil
}

func listVolumes(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
	volumes, err := client.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(volumes))
	for _, v := range volumes {
		names = append(names, v.Name+"\t"+size(v.SizeBytes))
	}

	return names, nil
}

func listKernels(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
	kernels, err := client.ListKernels(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(kernels))
	for _, k := range kernels {
		names = append(names, k.Name+"\t"+k.Arch)
	}

	return names, nil
}

// completeSnapshotArgs completes 'INSTANCE NAME': an instance, then one of
// its snapshots.
func completeSnapshotArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return complete(1, instancesIn())(cmd, args, toComplete)
	}

	return complete(2, func(ctx context.Context, client *dicer.Client, args []string) ([]string, error) {
		snapshots, err := client.ListSnapshots(ctx, args[0])
		if err != nil {
			return nil, err
		}

		names := make([]string, 0, len(snapshots))
		for _, s := range snapshots {
			names = append(names, s.Name+"\t"+age(s.CreatedAt))
		}

		return names, nil
	})(cmd, args, toComplete)
}

// completeRemotes completes the name of a configured remote. They live on
// this machine, so no daemon is asked.
func completeRemotes(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	dir, err := remote.Dir()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := remote.Load(dir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, name := range cfg.Names() {
		r, _ := cfg.Get(name)
		names = append(names, name+"\t"+r.Address)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// fixedCompletions completes a flag from a fixed set of values.
func fixedCompletions(values ...string) cobra.CompletionFunc {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}
