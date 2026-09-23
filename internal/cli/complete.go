// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/remote"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// completionTimeout bounds how long a completion waits on the daemon. A
// shell that hangs on <Tab> is worse than one that offers nothing.
const completionTimeout = 2 * time.Second

// completer lists the names a completion offers, each with a description
// after a tab.
type completer func(ctx context.Context, client *dicer.Client, args []string) ([]string, error)

// complete turns a completer into a cobra completion function for the first
// maxArgs arguments (all if 0), skipping names already given.
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

// instancesIn completes instance names, limited to the given states if any.
func instancesIn(states ...dicerdv1.InstanceState) completer {
	return func(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
		resp, err := client.ListInstances(ctx, &dicerdv1.ListInstancesRequest{})
		if err != nil {
			return nil, err
		}

		var names []string
		for _, inst := range resp.GetInstances() {
			if len(states) > 0 && !slices.Contains(states, inst.GetState()) {
				continue
			}
			names = append(names, inst.GetName()+"\t"+stateName(inst.GetState())+", "+inst.GetImageRef())
		}

		return names, nil
	}
}

func listImages(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
	resp, err := client.ListImages(ctx, &dicerdv1.ListImagesRequest{})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resp.GetImages()))
	for _, img := range resp.GetImages() {
		names = append(names, img.GetName()+"\t"+size(img.GetSizeBytes()))
	}

	return names, nil
}

func listNetworks(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
	resp, err := client.ListNetworks(ctx, &dicerdv1.ListNetworksRequest{})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resp.GetNetworks()))
	for _, n := range resp.GetNetworks() {
		names = append(names, n.GetName()+"\t"+n.GetSubnet())
	}

	return names, nil
}

func listVolumes(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
	resp, err := client.ListVolumes(ctx, &dicerdv1.ListVolumesRequest{})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resp.GetVolumes()))
	for _, v := range resp.GetVolumes() {
		names = append(names, v.GetName()+"\t"+size(v.GetSizeBytes()))
	}

	return names, nil
}

func listKernels(ctx context.Context, client *dicer.Client, _ []string) ([]string, error) {
	resp, err := client.ListKernels(ctx, &dicerdv1.ListKernelsRequest{})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resp.GetKernels()))
	for _, k := range resp.GetKernels() {
		names = append(names, k.GetName()+"\t"+archName(k.GetArch()))
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
		resp, err := client.ListSnapshots(ctx, &dicerdv1.ListSnapshotsRequest{Instance: args[0]})
		if err != nil {
			return nil, err
		}

		names := make([]string, 0, len(resp.GetSnapshots()))
		for _, s := range resp.GetSnapshots() {
			names = append(names, s.GetName()+"\t"+age(timeOf(s.GetCreateTime())))
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
